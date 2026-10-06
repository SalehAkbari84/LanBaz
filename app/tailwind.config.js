/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Base surfaces: a deep blue-black, not pure black, so glass panels
        // and shadows still read.
        surface: {
          DEFAULT: '#0b0e17',
          raised: '#121726',
          sunken: '#080a11',
          border: '#222a3d',
        },
        accent: {
          DEFAULT: '#7c5cff',
          2: '#22d3ee',
          soft: '#251d4d',
        },
        status: {
          running: '#34d399',
          stopping: '#fbbf24',
          stopped: '#8b93a7',
          error: '#f87171',
        },
      },
      fontFamily: {
        sans: ['Inter', 'Vazirmatn', 'ui-sans-serif', 'system-ui', 'Segoe UI', 'sans-serif'],
        fa: ['Vazirmatn', 'Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      boxShadow: {
        glow: '0 0 0 1px rgba(124,92,255,.35), 0 8px 30px -8px rgba(124,92,255,.45)',
        card: '0 1px 0 0 rgba(255,255,255,.04) inset, 0 10px 30px -12px rgba(0,0,0,.6)',
      },
      backgroundImage: {
        'accent-gradient': 'linear-gradient(135deg, #7c5cff 0%, #22d3ee 100%)',
        'app-gradient':
          'radial-gradient(1200px 600px at 100% -10%, rgba(124,92,255,.16), transparent 60%), radial-gradient(900px 500px at -10% 110%, rgba(34,211,238,.10), transparent 60%)',
      },
      keyframes: {
        pulseDot: {
          '0%, 100%': { opacity: '1' },
          '50%': { opacity: '.35' },
        },
      },
      animation: {
        'pulse-dot': 'pulseDot 1.6s ease-in-out infinite',
      },
    },
  },
  plugins: [],
}
