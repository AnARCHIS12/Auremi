# Aurémi 0.4.0

This release adds a graphical manager for updating and uninstalling integrated AppImages.

## Highlights

- Opens the application manager directly from the Aurémi menu entry.
- Lists integrated applications with their detected versions.
- Updates an application from a newly downloaded AppImage without creating a duplicate entry.
- Preserves the managed identity, launcher, icons, and desktop shortcut during updates.
- Uninstalls managed applications after confirmation.
- Preserves original downloads and application data during both updates and removal.
- Adds matching `manage`, `update`, and existing `remove` command-line operations.
- Signs every executable, installer archive, DEB, RPM, and checksum file with keyless Sigstore signing.
- Publishes a `.sigstore.json` verification bundle beside every signed download.

## Downloads

- **Debian, Ubuntu, Linux Mint:** choose the `.deb` file for your processor.
- **Fedora, openSUSE:** choose the `.rpm` file for your processor.
- **Other distributions:** choose the universal `Auremi-Installer` archive for your processor.

Most computers use AMD64. ARM64 packages are also provided.

After installation, open **Aurémi** from the application menu to manage integrated applications, or simply double-click an AppImage to install it.

Use `SHA256SUMS` to verify downloaded assets.

For the trusted software-center experience, install Aurémi from the signed repository by following the README. Direct release downloads remain available and continue to include Sigstore provenance bundles.
