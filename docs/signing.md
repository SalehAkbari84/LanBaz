# Signing the LanBaz installer / امضای نصب‌کنندهٔ LanBaz

## فارسی

### چرا ویندوز می‌گوید «Unknown publisher»؟
ویندوز (SmartScreen) برنامه‌ای را که **امضای دیجیتال کد (Authenticode)** ندارد «ناشر ناشناس» نشان می‌دهد. نصب‌کنندهٔ LanBaz دو امضای جدا دارد:

| امضا | کارش | وضعیت |
|---|---|---|
| امضای به‌روزرسانی (`updater.key`) | ثابت می‌کند فایل آپدیت واقعاً از توست | ✅ فعال است و باید بماند |
| Authenticode (گواهی ناشر) | نام ناشر را به ویندوز نشان می‌دهد | ❌ گواهی لازم دارد |

تا وقتی گواهی نداری، کاربران با **More info ← Run anyway** نصب می‌کنند. برنامه و امنیتش هیچ فرقی نمی‌کند.

### راه‌ها
1. **SignPath Foundation (رایگان برای پروژه‌های متن‌باز).** باید مخزن عمومی و مجوز متن‌باز داشته باشی و درخواست بدهی. امضا روی سرور آن‌ها و معمولاً از طریق GitHub Actions انجام می‌شود.
2. **Azure Trusted Signing (پولی، ارزان).** اشتراک ماهانه و احراز هویت می‌خواهد و ممکن است در همهٔ کشورها در دسترس نباشد.
3. **گواهی OV معمولی** از فروشنده‌های گواهی (سالانه، گران‌تر). از ۲۰۲۳ به بعد کلید این گواهی‌ها باید روی توکن سخت‌افزاری یا سرویس ابری باشد.

حتی با گواهی هم SmartScreen چند هفته طول می‌کشد تا به ناشر جدید «اعتبار» بدهد. هر چه دانلود بیشتر شود، هشدار زودتر از بین می‌رود.

### وقتی گواهی گرفتی
اسکریپت ساخت خودش امضا می‌کند. یکی از این‌ها را قبل از ساخت تنظیم کن:

```powershell
# گواهی نصب‌شده در Windows (Thumbprint از certmgr.msc)
$env:LANBAZ_SIGN_THUMBPRINT = "ABCDEF0123..."
# یا یک ابزار امضا (Azure Trusted Signing، SignPath، signtool...)؛ %1 = فایل
$env:LANBAZ_SIGN_COMMAND = "trusted-signing-cli -e https://xxx.codesigning.azure.net -a ACCOUNT -c PROFILE %1"
scripts\build-installer.ps1 -UpdateRepo SalehAkbari84/LanBaz
```

خروجی ساخت باید `Authenticode: signing with ...` را نشان دهد. بعد در ویندوز روی setup.exe کلیک راست کن ← Properties ← Digital Signatures.

## English

**Why "Unknown publisher":** the installer has no Authenticode signature. The *update* signature (`updater.key`, verified by the in-app updater) is separate, is already in place and must stay.

**Options:**
- SignPath Foundation: free for OSS; apply with a public repo and an OSI license; signs in CI.
- Azure Trusted Signing: low monthly fee, identity validation, regional availability varies.
- A regular OV certificate: annual; the key must live on hardware or a cloud HSM.

SmartScreen reputation still builds up over time and downloads.

**Build integration:** `scripts/build-installer.ps1` signs when one of these is set:
- `LANBAZ_SIGN_THUMBPRINT`: certificate in the Windows store, SHA-256, timestamped. The timestamp server defaults to DigiCert and can be overridden with `LANBAZ_SIGN_TIMESTAMP`.
- `LANBAZ_SIGN_COMMAND`: any signing command line, with `%1` standing for the file. It is passed to Tauri as `bundle.windows.signCommand`.

Without either, nothing changes.
