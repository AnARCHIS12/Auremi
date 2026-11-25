# Aurémi 0.4.1

This release adds full COSMIC desktop icon support while preserving full compatibility with KDE, GNOME, and other Freedesktop environments.

## Highlights

- Uses standard Freedesktop themed icon names (`Icon=<id>` and `Icon=auremi`) in generated `.desktop` launchers so COSMIC's launcher, app library, dock, and file manager resolve application icons reliably.
- Normalizes oversized (such as `1024x1024`) and non-square PNG icons into standard `hicolor` theme directories (`512x512`, `256x256`, `128x128`, `64x64`).
- Refreshes desktop and icon directory timestamps alongside `update-desktop-database`, `gtk-update-icon-cache`, and `kbuildsycoca6`.
- Adds `appimage-integrator repair-icons` (also run automatically on `setup --user`) to migrate existing integrated applications.
- Signs every executable, installer archive, DEB, RPM, and checksum file with keyless Sigstore signing.

## Downloads

- **Debian, Ubuntu, Linux Mint:** choose the `.deb` file for your processor.
- **Fedora, openSUSE:** choose the `.rpm` file for your processor.
- **Other distributions:** choose the universal `Auremi-Installer` archive for your processor.

Most computers use AMD64. ARM64 packages are also provided.

After installation, open **Aurémi** from the application menu to manage integrated applications, or simply double-click an AppImage to install it.

Use `SHA256SUMS` to verify downloaded assets.

For the trusted software-center experience, install Aurémi from the signed repository by following the README. Direct release downloads remain available and continue to include Sigstore provenance bundles.
