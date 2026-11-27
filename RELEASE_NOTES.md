# Aurémi 0.4.3

## What's New

- **COSMIC / hicolor icon fix**: automatically creates `~/.local/share/icons/hicolor/index.theme` when missing so themed AppImage icons resolve reliably in COSMIC and other desktop launchers.
- **Complete PNG sizes alongside SVG**: generates missing `48x48` and `32x32` PNG icons even when an SVG icon is bundled in the AppImage.

---

# Aurémi 0.4.2

This release ensures newly integrated AppImages and their icons appear immediately in COSMIC's dock and taskbar, and fixes the GitHub Pages repository URLs.

## Highlights

- Generates `48x48` and `32x32` PNG icon sizes in addition to `512x512`, `256x256`, `128x128`, and `64x64`.
- Refreshes `cosmic-panel` automatically under COSMIC sessions when integrating, updating, repairing, or removing an AppImage so `cosmic-app-list` picks up new icons immediately.
- Updates `auremi.repo`, `auremi.sources`, and `README.md` to point to `https://anarchis12.github.io/Auremi/`.

## Downloads

- **Debian, Ubuntu, Linux Mint:** choose the `.deb` file for your processor.
- **Fedora, openSUSE:** choose the `.rpm` file for your processor.
- **Other distributions:** choose the universal `Auremi-Installer` archive for your processor.

Most computers use AMD64. ARM64 packages are also provided.

After installation, open **Aurémi** from the application menu to manage integrated applications, or simply double-click an AppImage to install it.

Use `SHA256SUMS` to verify downloaded assets.

For the trusted software-center experience, install Aurémi from the signed repository by following the README. Direct release downloads remain available and continue to include Sigstore provenance bundles.
