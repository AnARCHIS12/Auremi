package main

import (
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	got := slugify(" GitHub Copilot — édition 2026 ")
	if got != "github-copilot-dition-2026" {
		t.Fatalf("slugify() = %q", got)
	}
}

func TestUpdateINIListPreservesExistingValues(t *testing.T) {
	in := "[Default Applications]\ntext/plain=editor.desktop;\napplication/vnd.appimage=old.desktop;\n"
	out := updateINIList(in, "Default Applications", "application/vnd.appimage", handlerID)
	if !strings.Contains(out, "application/vnd.appimage="+handlerID+";old.desktop;") {
		t.Fatalf("association incorrecte:\n%s", out)
	}
	if !strings.Contains(out, "text/plain=editor.desktop;") {
		t.Fatal("l’association existante a été perdue")
	}
}

func TestValidSquashSuperblock(t *testing.T) {
	b := make([]byte, 96)
	copy(b, "hsqs")
	binary.LittleEndian.PutUint32(b[12:16], 131072)
	binary.LittleEndian.PutUint16(b[28:30], 4)
	binary.LittleEndian.PutUint16(b[30:32], 0)
	binary.LittleEndian.PutUint64(b[40:48], 4096)
	if !validSquashSuperblock(b, 8192) {
		t.Fatal("superbloc valide refusé")
	}
	binary.LittleEndian.PutUint64(b[40:48], 9000)
	if validSquashSuperblock(b, 8192) {
		t.Fatal("superbloc hors limites accepté")
	}
}

func TestValidateAppImageHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.AppImage")
	b := make([]byte, 4096)
	copy(b[:4], "\x7fELF")
	copy(b[8:11], []byte{'A', 'I', 2})
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateAppImageHeader(path); err != nil {
		t.Fatal(err)
	}
}

func TestPathContainment(t *testing.T) {
	root := "/tmp/apps"
	if !isWithin("/tmp/apps/demo", root) {
		t.Fatal("chemin enfant refusé")
	}
	if isWithin("/tmp/apps-evil/demo", root) || isWithin("/tmp/apps/../secret", root) {
		t.Fatal("sortie du dossier racine acceptée")
	}
}

func TestFindBestIconsUsesDeclaredNameAndKeepsSizes(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "usr/share/icons/hicolor/64x64/apps/right-logo.png"), 64)
	writeTestPNG(t, filepath.Join(root, "usr/share/icons/hicolor/256x256/apps/right-logo.png"), 256)
	writeTestPNG(t, filepath.Join(root, "unrelated-huge.png"), 512)

	icons := findBestIcons(root, desktopMetadata{Icon: "right-logo", Name: "Example"})
	if len(icons) != 2 {
		t.Fatalf("got %d matching icons, want 2", len(icons))
	}
	for _, icon := range icons {
		if filepath.Base(icon.path) != "right-logo.png" {
			t.Fatalf("selected unrelated icon %q", icon.path)
		}
	}
}

func TestFindBestIconsRecognizesDirIconWithoutExtension(t *testing.T) {
	root := t.TempDir()
	icon := filepath.Join(root, ".DirIcon")
	writeTestPNG(t, icon, 128)

	icons := findBestIcons(root, desktopMetadata{Name: "No matching filename"})
	if len(icons) != 1 || icons[0].path != icon || icons[0].ext != ".png" {
		t.Fatalf(".DirIcon not recognized: %#v", icons)
	}
}

func TestPreferredDesktopEntryPreservesWaylandAppID(t *testing.T) {
	apps := t.TempDir()
	got := preferredDesktopEntry(apps, "com.example.Editor.desktop", "appimage-editor", "/tmp/Editor.AppImage")
	want := filepath.Join(apps, "com.example.Editor.desktop")
	if got != want {
		t.Fatalf("preferredDesktopEntry() = %q, want %q", got, want)
	}

	if err := os.WriteFile(want, []byte("[Desktop Entry]\nName=Other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = preferredDesktopEntry(apps, "com.example.Editor.desktop", "appimage-editor", "/tmp/Editor.AppImage")
	if got != filepath.Join(apps, "appimage-editor.desktop") {
		t.Fatalf("existing launcher was not protected: %q", got)
	}
}

func TestFindAndParseDesktopKeepsDesktopID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "org.example.App.desktop")
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nName=Example\nIcon=example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := findAndParseDesktop(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.DesktopID != "org.example.App.desktop" {
		t.Fatalf("DesktopID = %q", m.DesktopID)
	}
}

func TestParseDesktopFileReadsAppImageVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.desktop")
	data := "[Desktop Entry]\nName=Example\nVersion=1.0\nX-AppImage-Version=4.6.2-beta.1\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := parseDesktopFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "4.6.2-beta.1" {
		t.Fatalf("Version = %q", m.Version)
	}
}

func TestFindAppStreamVersionMatchesDesktopID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "usr/share/metainfo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"dependency.xml": `<component><id>org.example.Dependency</id><releases><release version="99.0"/></releases></component>`,
		"editor.xml":     `<component><id>org.example.Editor</id><releases><release version="7.4.1"/></releases></component>`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := findAppStreamVersion(root, "org.example.Editor.desktop"); got != "7.4.1" {
		t.Fatalf("findAppStreamVersion() = %q", got)
	}
}

func TestVersionFromFilename(t *testing.T) {
	if got := versionFromFilename("/tmp/Example-Studio-3.12.4-beta.2-x86_64.AppImage"); got != "3.12.4-beta.2" {
		t.Fatalf("versionFromFilename() = %q", got)
	}
	if got := versionFromFilename("/tmp/Example-Studio.AppImage"); got != "" {
		t.Fatalf("versionFromFilename() invented version %q", got)
	}
}

func TestInstalledDesktopEntryIncludesApplicationVersion(t *testing.T) {
	body := installedDesktopEntry(desktopMetadata{Name: "Example", Version: "2.8.0", Categories: "Utility;"}, "/tmp/Example.AppImage", "/tmp/example.png", "/tmp/source.AppImage")
	if !strings.Contains(body, "X-AppImage-Version=2.8.0\n") {
		t.Fatalf("application version missing:\n%s", body)
	}
	if !strings.Contains(body, "X-AppImage-Integrator-Version="+version+"\n") {
		t.Fatalf("integrator version missing:\n%s", body)
	}
}

func TestUpdateKeepsManagedIdentityAndPreservesSources(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	firstSource := filepath.Join(root, "first", "Demo.AppImage")
	secondSource := filepath.Join(root, "second", "Demo.AppImage")
	writeTestAppImage(t, firstSource, 1)
	writeTestAppImage(t, secondSource, 2)

	first, err := integrate(firstSource, false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := integrateWithID(secondSource, false, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.DesktopFile != first.DesktopFile {
		t.Fatalf("managed identity changed: before=%#v after=%#v", first, updated)
	}
	if updated.Source != secondSource || updated.SHA256 == first.SHA256 {
		t.Fatalf("updated AppImage was not recorded: before=%#v after=%#v", first, updated)
	}
	if err := removeCommand([]string{updated.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(updated.Installed)); !os.IsNotExist(err) {
		t.Fatalf("managed application directory still exists: %v", err)
	}
	for _, source := range []string{firstSource, secondSource} {
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("source file was removed: %s", source)
		}
	}
}

func writeTestAppImage(t *testing.T, path string, marker byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	copy(body[:4], "\x7fELF")
	copy(body[8:11], []byte{'A', 'I', 2})
	body[len(body)-1] = marker
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestPNG(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIconsNormalizesOversizedPNGForCosmic(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	srcIcon := filepath.Join(root, "app.png")
	writeTestPNG(t, srcIcon, 1024)

	installed := installIcons([]iconCandidate{{path: srcIcon, ext: ".png", sizeDir: iconSizeDir(srcIcon)}}, dataHome, "appimage-demo")
	if len(installed) == 0 {
		t.Fatal("expected installed icons")
	}
	for _, p := range installed {
		if strings.Contains(p, "1024x1024") {
			t.Fatalf("oversized 1024x1024 icon should not be installed in hicolor: %s", p)
		}
	}
	for _, wantSize := range []string{"512x512", "256x256", "128x128", "64x64"} {
		wantPath := filepath.Join(dataHome, "icons", "hicolor", wantSize, "apps", "appimage-demo.png")
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("missing normalized icon %s: %v", wantPath, err)
		}
	}
}

func TestIntegrateUsesThemedIconNameInDesktopEntry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	source := filepath.Join(root, "Demo.AppImage")
	writeTestAppImage(t, source, 1)

	res, err := integrate(source, false)
	if err != nil {
		t.Fatal(err)
	}
	desktopData, err := os.ReadFile(res.DesktopFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktopData), "Icon="+res.ID+"\n") {
		t.Fatalf("desktop entry should reference themed icon name %q:\n%s", res.ID, string(desktopData))
	}
	if res.Icon == "" || !filepath.IsAbs(res.Icon) {
		t.Fatalf("metadata should keep absolute icon file path, got %q", res.Icon)
	}
}

func TestRepairInstalledIconsMigratesAbsoluteAndOversizedIcons(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "data")
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	p, err := userPaths()
	if err != nil {
		t.Fatal(err)
	}
	id := "appimage-legacy"
	appDir := filepath.Join(p.appsRoot, id)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.appEntries, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyIcon := filepath.Join(p.iconsRoot, "hicolor", "1024x1024", "apps", id+".png")
	writeTestPNG(t, legacyIcon, 1024)

	desktopPath := filepath.Join(p.appEntries, "legacy.desktop")
	if err := os.WriteFile(desktopPath, []byte("[Desktop Entry]\nName=Legacy\nIcon="+legacyIcon+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := appMetadata{
		ID:          id,
		Name:        "Legacy",
		Source:      "/tmp/Legacy.AppImage",
		Installed:   filepath.Join(appDir, "Legacy.AppImage"),
		Icon:        legacyIcon,
		Icons:       []string{legacyIcon},
		DesktopFile: desktopPath,
	}
	encoded, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(appDir, "metadata.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	repaired, err := repairInstalledIcons(p)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 {
		t.Fatalf("repaired = %d, want 1", repaired)
	}
	if _, err := os.Stat(legacyIcon); !os.IsNotExist(err) {
		t.Fatalf("legacy 1024x1024 icon should have been removed: %v", err)
	}
	desktopData, err := os.ReadFile(desktopPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktopData), "Icon="+id+"\n") {
		t.Fatalf("desktop entry not migrated to themed icon:\n%s", string(desktopData))
	}
	if _, err := os.Stat(filepath.Join(p.iconsRoot, "hicolor", "256x256", "apps", id+".png")); err != nil {
		t.Fatalf("expected 256x256 icon after repair: %v", err)
	}
}
