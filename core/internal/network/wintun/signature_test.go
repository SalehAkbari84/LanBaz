package wintun

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// The Wintun API is bound by hand, so nothing about it is checked by the
// compiler. A call with the wrong handle or the wrong number of arguments is a
// perfectly well-formed call that the driver rejects at run time - and it
// rejects it *after* the adapter has been created, configured and reported as
// ready, which is the most expensive possible moment to find out.
//
// That is not hypothetical. Every packet function in this file was originally
// given the adapter handle instead of the session handle, and WintunStartSession
// was called with one argument instead of two. The network reported itself
// ready, the room existed, the interface had an address and a route, and not one
// packet ever moved.
//
// These tests read the source and check the call sites, because that is the
// only place the mistake can be caught before it reaches a driver.

// upstream declares, for every export the binding uses, how many arguments
// wintun.h gives it and which handle it expects. Taken from include/wintun.h in
// third_party/wintun.
var (
	// sessionProcs take the session handle WintunStartSession returned.
	sessionProcs = map[string]int{
		"WintunStartSession":         2,
		"WintunEndSession":           1,
		"WintunGetReadWaitEvent":     1,
		"WintunReceivePacket":        2,
		"WintunReleaseReceivePacket": 2,
		"WintunAllocateSendPacket":   2,
		"WintunSendPacket":           2,
	}
	// adapterProcs take the adapter handle.
	adapterProcs = map[string]int{
		"WintunCreateAdapter":  3,
		"WintunCloseAdapter":   1,
		"WintunOpenAdapter":    1,
		"WintunGetAdapterLUID": 2,
	}
)

// sessionProcsThatTakeTheAdapter are the session calls whose first argument is
// the adapter, because they are the ones that create or destroy the session
// rather than using one. WintunStartSession is the boundary: its second argument
// is the ring capacity, which is what its predecessor's missing argument turned
// out to be, and that is why the arity of every one of these is pinned here.
var sessionProcsThatTakeTheAdapter = map[string]bool{
	"WintunStartSession": true,
}

// binding returns the parsed binding plus the source text of a call node.
type source struct {
	fset *token.FileSet
	file *ast.File
	text map[ast.Node]string
}

func parseBinding(t *testing.T) source {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wintun_windows.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot parse the binding: %v", err)
	}
	return source{fset: fset, file: file, text: map[ast.Node]string{}}
}

func (s source) render(n ast.Node) string {
	if txt, ok := s.text[n]; ok {
		return txt
	}
	var b strings.Builder
	if err := printer.Fprint(&b, s.fset, n); err != nil {
		return "<unprintable>"
	}
	s.text[n] = b.String()
	return s.text[n]
}

// exports maps each LazyProc variable name to the DLL export it is bound to, so
// the test reads the binding's own declarations instead of a second table that
// could drift away from them.
func (s source) exports() map[string]string {
	out := map[string]string{}
	ast.Inspect(s.file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				continue
			}
			// procWintunXxx = wintunDLL.NewProc("WintunXxx")
			call, ok := vs.Values[i].(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewProc" {
				continue
			}
			arg, ok := call.Args[0].(*ast.BasicLit)
			if !ok || arg.Kind != token.STRING {
				continue
			}
			export, err := strconv.Unquote(arg.Value)
			if err != nil {
				continue
			}
			out[name.Name] = export
		}
		return true
	})
	return out
}

func TestWintunCallsMatchUpstreamSignatures(t *testing.T) {
	src := parseBinding(t)
	procs := src.exports()
	if len(procs) == 0 {
		t.Fatal("no LazyProc bindings found; this test would pass without checking anything")
	}

	checked := 0
	ast.Inspect(src.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Only the wrapper counts: call(procWintunXxx, args...). Calls like
		// procWintunXxx.Find() are resolver calls, not driver calls, and carry
		// none of the arguments this test is about.
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "call" || len(call.Args) == 0 {
			return true
		}
		ident, ok := call.Args[0].(*ast.Ident)
		if !ok {
			return true
		}
		export, bound := procs[ident.Name]
		if !bound {
			return true
		}

		wantArgs, takesSession := sessionProcs[export]
		adapterArgs, takesAdapter := adapterProcs[export]
		if takesAdapter {
			wantArgs, takesSession = adapterArgs, false
		}
		if !takesSession && !takesAdapter {
			// An export with no upstream declaration recorded: say so rather
			// than quietly testing nothing.
			t.Errorf("%s is bound but has no signature recorded in signature_test.go", export)
			return true
		}
		checked++

		// call.Args[0] is the LazyProc itself; the driver arguments follow it.
		if got := len(call.Args) - 1; got != wantArgs {
			t.Errorf("%s is called with %d argument(s), upstream declares %d\n\t%s",
				export, got, wantArgs, src.fset.Position(call.Pos()))
		}
		if takesSession && len(call.Args) > 1 && !sessionProcsThatTakeTheAdapter[export] {
			// Both handles are pointers, so handing the adapter to a session
			// call has no compile-time signal at all.
			if arg := src.render(call.Args[1]); strings.Contains(arg, ".handle") {
				t.Errorf("%s is given the adapter handle, but upstream declares it takes the session handle\n\t%s",
					export, src.fset.Position(call.Pos()))
			}
		}
		if takesSession && len(call.Args) > 1 && !sessionProcsThatTakeTheAdapter[export] {
			if arg := src.render(call.Args[1]); !strings.Contains(arg, "session") {
				t.Errorf("%s is not given the session handle\n\t%s",
					export, src.fset.Position(call.Pos()))
			}
		}
		return true
	})

	if checked < 8 {
		t.Errorf("only %d Wintun calls were inspected; the binding is expected to make at least 8", checked)
	}
}

// TestEveryExportIsAccountedFor stops a newly bound export from slipping in
// without a signature to check it against.
func TestEveryExportIsAccountedFor(t *testing.T) {
	src := parseBinding(t)
	for _, export := range src.exports() {
		_, inSession := sessionProcs[export]
		_, inAdapter := adapterProcs[export]
		if !inSession && !inAdapter {
			t.Errorf("%s is bound in wintun_windows.go but no signature is recorded for it in signature_test.go", export)
		}
	}
	if len(src.exports()) < 10 {
		t.Errorf("only %d exports are bound; the binding is expected to use at least 10", len(src.exports()))
	}
}
