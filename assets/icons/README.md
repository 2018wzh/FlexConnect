# FlexConnect icon assets

This directory contains the current FlexConnect brand assets derived from the user-provided logo.

- `app.svg`: square application icon used for tray, favicon, and executable icon generation
- `logo.svg`: horizontal product logo used in the local dashboard header
- `favicon.ico`, `favicon-32.png`, `tray.ico`: generated runtime assets

Regenerate the brand assets, bitmaps, and `.ico` outputs with:

```powershell
go run .\scripts\generate-icons.go
```
