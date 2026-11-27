package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	programName = "appimage-integrator"
	productName = "Aurémi"
	version     = "0.4.2"
	handlerID   = "appimage-integrator-handler.desktop"
	launcherID  = "io.github.anarchis12.auremi.desktop"
	metainfoID  = "io.github.anarchis12.auremi.metainfo.xml"
)

//go:embed assets/auremi-logo-512.png
var auremiLogo []byte

//go:embed packaging/io.github.anarchis12.auremi.metainfo.xml
var auremiMetainfo []byte

type appMetadata struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Comment     string   `json:"comment,omitempty"`
	Source      string   `json:"source"`
	Installed   string   `json:"installed"`
	SHA256      string   `json:"sha256"`
	Integrated  string   `json:"integrated_at"`
	Icon        string   `json:"icon,omitempty"`
	Icons       []string `json:"icons,omitempty"`
	DesktopFile string   `json:"desktop_file"`
}

type desktopMetadata struct {
	Name           string
	Version        string
	Comment        string
	Categories     string
	StartupWMClass string
	Icon           string
	DesktopID      string
}

type iconCandidate struct {
	path    string
	ext     string
	sizeDir string
	score   int64
}

type paths struct {
	home       string
	dataHome   string
	configHome string
	desktopDir string
	appsRoot   string
	appEntries string
	iconsRoot  string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return welcomeCommand()
	}
	switch args[0] {
	case "integrate", "install":
		return integrateCommand(args[1:])
	case "setup":
		return setupCommand(args[1:])
	case "welcome":
		return welcomeCommand()
	case "manage":
		return manageCommand()
	case "list":
		return listCommand()
	case "update":
		return updateCommand(args[1:])
	case "remove", "uninstall":
		return removeCommand(args[1:])
	case "repair", "repair-icons":
		return repairCommand()
	case "version", "--version", "-v":
		fmt.Println(programName, version)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	default:
		if strings.HasSuffix(strings.ToLower(args[0]), ".appimage") {
			return integrateCommand(args)
		}
		return usageError()
	}
}

func usageError() error {
	printUsage(os.Stderr)
	return errors.New("missing or unknown command")
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `%s %s — seamless AppImage integration

Usage:
  %s setup --user              Install the handler for the current user
  sudo %s setup --system       Install the handler for every user
  %s integrate file.AppImage
  %s update ID file.AppImage
  %s manage
  %s list
  %s remove ID
  %s repair-icons

Integration never launches the AppImage and never deletes the source file.
`, programName, version, programName, programName, programName, programName, programName, programName, programName, programName)
}

func welcomeCommand() error {
	message := fmt.Sprintf("Aurémi %s\n\nDouble-click a downloaded AppImage to install it.\n\nOpen the application manager to update or uninstall integrated applications.\nThe original downloaded files are always preserved.", version)
	if os.Getenv("AUREMI_NO_DIALOG") == "1" {
		fmt.Println(message)
		return nil
	}
	if dialogQuestion(message, "Manage applications", "Close") {
		return manageCommand()
	}
	return nil
}

func integrateCommand(args []string) error {
	set := flag.NewFlagSet("integrate", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	noDesktop := set.Bool("no-desktop", false, "do not create a desktop shortcut")
	quiet := set.Bool("quiet", false, "do not display a notification")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return errors.New("provide exactly one AppImage file")
	}
	result, err := integrate(set.Arg(0), !*noDesktop)
	if err != nil {
		if !*quiet {
			notify("AppImage installation failed", err.Error(), "dialog-error")
		}
		return err
	}
	fmt.Printf("%s installed\nID: %s\n", result.Name, result.ID)
	if result.Version != "" {
		fmt.Printf("Version: %s\n", result.Version)
	}
	fmt.Printf("Location: %s\n", result.Installed)
	if !*quiet {
		notify("AppImage installed", result.Name+" is now available in the application menu.", result.Icon)
	}
	return nil
}

func integrate(input string, desktopShortcut bool) (*appMetadata, error) {
	return integrateWithID(input, desktopShortcut, "")
}

func integrateWithID(input string, desktopShortcut bool, forcedID string) (*appMetadata, error) {
	source, err := filepath.Abs(input)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("file is inaccessible: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symbolic links are not accepted; select the actual AppImage file")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the source is not a regular file")
	}
	if info.Size() < 4096 {
		return nil, errors.New("the file is too small to be a valid AppImage")
	}
	if err := validateAppImageHeader(source); err != nil {
		return nil, err
	}

	p, err := userPaths()
	if err != nil {
		return nil, err
	}
	meta := desktopMetadata{
		Name:       cleanDisplayName(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))),
		Comment:    "Locally integrated AppImage application",
		Categories: "Utility;",
	}
	tmp, err := os.MkdirTemp("", "appimage-integrator-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	extracted, extractErr := extractMetadata(source, tmp)
	metadataIdentified := false
	if extractErr == nil {
		if parsed, err := findAndParseDesktop(extracted); err == nil {
			mergeDesktopMetadata(&meta, parsed)
			metadataIdentified = true
		}
		if meta.Version == "" {
			meta.Version = findAppStreamVersion(extracted, meta.DesktopID)
		}
	}
	if meta.Name == "" {
		meta.Name = "AppImage"
	}
	if meta.Version == "" {
		meta.Version = versionFromFilename(source)
	}
	id := forcedID
	var previous appMetadata
	if id == "" {
		id = uniqueID(slugify(meta.Name), source, p.appsRoot)
	} else {
		previous, err = managedAppByID(p, id)
		if err != nil {
			return nil, err
		}
		if metadataIdentified && slugify(previous.Name) != slugify(meta.Name) {
			return nil, fmt.Errorf("selected file is %s, expected an update for %s", meta.Name, previous.Name)
		}
	}
	appDir := filepath.Join(p.appsRoot, id)
	installed := filepath.Join(appDir, safeAppImageFilename(meta.Name))
	entry := previous.DesktopFile
	if entry == "" || !isWithin(entry, p.appEntries) {
		entry = preferredDesktopEntry(p.appEntries, meta.DesktopID, id, source)
	}
	if previous.ID == "" {
		if data, err := os.ReadFile(filepath.Join(appDir, "metadata.json")); err == nil {
			_ = json.Unmarshal(data, &previous)
		}
	}

	for _, dir := range []string{appDir, p.appEntries, p.iconsRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := copyFileAtomic(source, installed, 0o755); err != nil {
		return nil, fmt.Errorf("copying the AppImage: %w", err)
	}

	iconPath := ""
	var iconPaths []string
	if extracted != "" {
		iconPaths = installIcons(findBestIcons(extracted, meta), p.dataHome, id)
		if len(iconPaths) > 0 {
			iconPath = iconPaths[0]
		}
	}
	if iconPath == "" {
		iconDir := filepath.Join(p.dataHome, "icons", "hicolor", "scalable", "apps")
		if err := os.MkdirAll(iconDir, 0o755); err != nil {
			return nil, err
		}
		iconPath = filepath.Join(iconDir, id+".svg")
		if err := writeAtomic(iconPath, []byte(genericIconSVG), 0o644); err != nil {
			return nil, err
		}
		iconPaths = []string{iconPath}
	}

	desktopBody := installedDesktopEntry(meta, installed, id, source)
	if err := writeAtomic(entry, []byte(desktopBody), 0o755); err != nil {
		return nil, err
	}
	if desktopShortcut && p.desktopDir != "" {
		if err := os.MkdirAll(p.desktopDir, 0o755); err == nil {
			shortcut := filepath.Join(p.desktopDir, safeDesktopFilename(meta.Name)+".desktop")
			_ = writeAtomic(shortcut, []byte(desktopBody), 0o755)
		}
	}

	hash, err := fileSHA256(installed)
	if err != nil {
		return nil, err
	}
	result := &appMetadata{
		ID:          id,
		Name:        meta.Name,
		Version:     meta.Version,
		Comment:     meta.Comment,
		Source:      source,
		Installed:   installed,
		SHA256:      hash,
		Integrated:  time.Now().Format(time.RFC3339),
		Icon:        iconPath,
		Icons:       iconPaths,
		DesktopFile: entry,
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	if err := writeAtomic(filepath.Join(appDir, "metadata.json"), append(encoded, '\n'), 0o644); err != nil {
		return nil, err
	}
	cleanupPreviousIntegration(previous, *result, p)
	refreshDesktop(p)
	return result, nil
}

func updateCommand(args []string) error {
	if len(args) != 2 {
		return errors.New("provide the managed application ID and a new AppImage file")
	}
	p, err := userPaths()
	if err != nil {
		return err
	}
	previous, err := managedAppByID(p, args[0])
	if err != nil {
		return err
	}
	shortcut := desktopShortcutExists(previous, p)
	result, err := integrateWithID(args[1], shortcut, previous.ID)
	if err != nil {
		return err
	}
	fmt.Printf("%s updated\n", result.Name)
	if result.Version != "" {
		fmt.Printf("Version: %s\n", result.Version)
	}
	return nil
}

func validateAppImageHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return errors.New("unable to read the AppImage header")
	}
	if string(header[:4]) != "\x7fELF" {
		return errors.New("the file is not an ELF executable")
	}
	if header[8] != 'A' || header[9] != 'I' || (header[10] != 1 && header[10] != 2) {
		return errors.New("missing AppImage signature or unsupported format")
	}
	return nil
}

func extractMetadata(source, tempRoot string) (string, error) {
	unsquashfs, err := exec.LookPath("unsquashfs")
	if err != nil {
		return "", errors.New("squashfs-tools is unavailable; using the generic icon")
	}
	offset, err := findSquashFSOffset(source)
	if err != nil {
		return "", err
	}
	payload := filepath.Join(tempRoot, "payload.squashfs")
	if err := copyFromOffset(source, payload, offset); err != nil {
		return "", err
	}
	out := filepath.Join(tempRoot, "metadata")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	patterns := []string{
		"*.desktop", ".DirIcon", "*.png", "*.svg", "*.xpm",
		"usr/share/applications/*.desktop",
		"usr/share/icons/hicolor/*/apps/*.png",
		"usr/share/icons/hicolor/*/apps/*.svg",
		"usr/share/icons/hicolor/*/apps/*.xpm",
		"usr/share/pixmaps/*",
		"usr/share/metainfo/*.xml",
		"usr/share/appdata/*.xml",
	}
	args := append([]string{"-no-progress", "-d", out, payload}, patterns...)
	cmd := exec.CommandContext(ctx, unsquashfs, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("timed out while reading metadata")
		}
		return "", fmt.Errorf("unable to read SquashFS data: %w", err)
	}
	return out, nil
}

func findSquashFSOffset(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const chunkSize = 4 << 20
	buf := make([]byte, chunkSize+95)
	var base int64
	var overlap int
	for {
		n, readErr := f.Read(buf[overlap:])
		total := overlap + n
		for start := 0; start+96 <= total; {
			i := bytesIndex(buf[start:total], []byte("hsqs"))
			if i < 0 {
				break
			}
			i += start
			candidate := base - int64(overlap) + int64(i)
			if validSquashSuperblock(buf[i:i+96], info.Size()-candidate) {
				return candidate, nil
			}
			start = i + 4
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, readErr
		}
		if total > 95 {
			overlap = 95
			copy(buf[:overlap], buf[total-overlap:total])
		} else {
			overlap = total
		}
		base += int64(n)
	}
	return 0, errors.New("AppImage SquashFS filesystem not found")
}

func bytesIndex(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func validSquashSuperblock(b []byte, remaining int64) bool {
	if len(b) < 96 || string(b[:4]) != "hsqs" {
		return false
	}
	blockSize := binary.LittleEndian.Uint32(b[12:16])
	major := binary.LittleEndian.Uint16(b[28:30])
	minor := binary.LittleEndian.Uint16(b[30:32])
	bytesUsed := binary.LittleEndian.Uint64(b[40:48])
	return major == 4 && minor == 0 && blockSize >= 4096 && blockSize <= 1<<20 && blockSize&(blockSize-1) == 0 && bytesUsed >= 96 && bytesUsed <= uint64(remaining)
}

func copyFromOffset(source, dest string, offset int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.NewSectionReader(in, offset, info.Size()-offset))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func findAndParseDesktop(root string) (desktopMetadata, error) {
	var candidates []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".desktop") {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil || len(candidates) == 0 {
		return desktopMetadata{}, errors.New("no embedded .desktop file found")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return desktopRank(candidates[i]) < desktopRank(candidates[j])
	})
	m, err := parseDesktopFile(candidates[0])
	if err == nil {
		m.DesktopID = safeDesktopID(filepath.Base(candidates[0]))
	}
	return m, err
}

func desktopRank(path string) int {
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/usr/share/applications/") {
		return 1
	}
	return 0
}

func parseDesktopFile(path string) (desktopMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return desktopMetadata{}, err
	}
	defer f.Close()
	var m desktopMetadata
	inMain := false
	s := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inMain = line == "[Desktop Entry]"
			continue
		}
		if !inMain || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "Name":
			if m.Name == "" {
				m.Name = cleanDisplayName(val)
			}
		case "Comment":
			if m.Comment == "" {
				m.Comment = cleanDesktopValue(val)
			}
		case "Categories":
			m.Categories = cleanCategories(val)
		case "StartupWMClass":
			m.StartupWMClass = cleanDesktopValue(val)
		case "Icon":
			m.Icon = cleanDesktopValue(val)
		case "X-AppImage-Version":
			m.Version = cleanAppVersion(val)
		}
	}
	return m, s.Err()
}

func mergeDesktopMetadata(dst *desktopMetadata, src desktopMetadata) {
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.Comment != "" {
		dst.Comment = src.Comment
	}
	if src.Categories != "" {
		dst.Categories = src.Categories
	}
	if src.StartupWMClass != "" {
		dst.StartupWMClass = src.StartupWMClass
	}
	dst.Icon = src.Icon
	dst.DesktopID = src.DesktopID
}

func findAppStreamVersion(root, desktopID string) string {
	type component struct {
		ID       string `xml:"id"`
		Releases []struct {
			Version string `xml:"version,attr"`
		} `xml:"releases>release"`
	}
	type candidate struct {
		id      string
		version string
	}
	var candidates []candidate
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".xml") {
			return nil
		}
		clean := filepath.ToSlash(path)
		if !strings.Contains(clean, "/usr/share/metainfo/") && !strings.Contains(clean, "/usr/share/appdata/") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var parsed component
		if xml.Unmarshal(data, &parsed) != nil || parsed.ID == "" {
			return nil
		}
		for _, release := range parsed.Releases {
			if version := cleanAppVersion(release.Version); version != "" {
				candidates = append(candidates, candidate{id: strings.TrimSpace(parsed.ID), version: version})
				break
			}
		}
		return nil
	})
	target := strings.TrimSuffix(desktopID, ".desktop")
	for _, candidate := range candidates {
		id := strings.TrimSuffix(candidate.id, ".desktop")
		if target != "" && strings.EqualFold(id, target) {
			return candidate.version
		}
	}
	if len(candidates) == 1 {
		return candidates[0].version
	}
	return ""
}

func cleanAppVersion(value string) string {
	value = cleanDesktopValue(value)
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func versionFromFilename(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	archSuffix := regexp.MustCompile(`(?i)(?:[-_.](?:x86_64|amd64|aarch64|arm64|i[3-6]86))$`)
	base = archSuffix.ReplaceAllString(base, "")
	versionPattern := regexp.MustCompile(`(?i)(?:^|[-_ ])v?([0-9]+(?:\.[0-9]+)+(?:[-+._][0-9A-Za-z]+)*)`)
	matches := versionPattern.FindAllStringSubmatch(base, -1)
	if len(matches) == 0 {
		return ""
	}
	return cleanAppVersion(matches[len(matches)-1][1])
}

func findBestIcons(root string, meta desktopMetadata) []iconCandidate {
	target := iconBase(meta.Icon)
	desktopBase := iconBase(meta.DesktopID)
	nameBase := normalizedIconName(meta.Name)
	var all []iconCandidate
	seen := make(map[string]int)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil || !isWithin(realPath, root) {
			return nil
		}
		info, err := os.Stat(realPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 10<<20 {
			return nil
		}
		ext := supportedIconExtension(realPath)
		if ext == "" {
			return nil
		}
		score := int64(0)
		base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		if target != "" && strings.EqualFold(base, target) {
			score = 4 << 40
		}
		if d.Name() == ".DirIcon" {
			score = max64(score, 3<<40)
		}
		if desktopBase != "" && strings.EqualFold(base, desktopBase) {
			score = max64(score, 2<<40)
		}
		if nameBase != "" && normalizedIconName(base) == nameBase {
			score = max64(score, 1<<40)
		}
		if score == 0 {
			return nil
		}
		if strings.Contains(filepath.ToSlash(realPath), "/usr/share/icons/") {
			score += 1 << 32
		}
		score += iconFormatScore(ext)
		score += min64(info.Size(), 1<<31)
		candidate := iconCandidate{path: realPath, ext: ext, sizeDir: iconSizeDir(realPath), score: score}
		if index, ok := seen[realPath]; ok {
			if candidate.score > all[index].score {
				all[index] = candidate
			}
			return nil
		}
		seen[realPath] = len(all)
		all = append(all, candidate)
		return nil
	})
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	bestClass := all[0].score >> 40
	var selected []iconCandidate
	seenSlots := make(map[string]bool)
	for _, candidate := range all {
		if candidate.score>>40 != bestClass {
			continue
		}
		slot := candidate.sizeDir + candidate.ext
		if !seenSlots[slot] {
			selected = append(selected, candidate)
			seenSlots[slot] = true
		}
	}
	return selected
}

var standardHicolorSizes = []int{16, 22, 24, 32, 36, 48, 64, 72, 96, 128, 192, 256, 512}
var cosmicRecommendedPNGSizes = []int{256, 128, 64, 48, 32}

func nearestHicolorSize(dim int) int {
	if dim >= 512 {
		return 512
	}
	for i := len(standardHicolorSizes) - 1; i >= 0; i-- {
		if standardHicolorSizes[i] <= dim {
			return standardHicolorSizes[i]
		}
	}
	return 16
}

func isStandardHicolorSize(w, h int) bool {
	if w != h {
		return false
	}
	for _, s := range standardHicolorSizes {
		if w == s {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// resizeImageNRGBA scales src proportionally into a transparent targetSize x targetSize square.
func resizeImageNRGBA(src image.Image, targetSize int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, targetSize, targetSize))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 || targetSize <= 0 {
		return dst
	}
	scale := float64(targetSize) / float64(maxInt(sw, sh))
	dw := int(float64(sw)*scale + 0.5)
	dh := int(float64(sh)*scale + 0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	offX := (targetSize - dw) / 2
	offY := (targetSize - dh) / 2

	srcNRGBA := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			srcNRGBA.Set(x, y, src.At(sb.Min.X+x, sb.Min.Y+y))
		}
	}

	for dy := 0; dy < dh; dy++ {
		sy := (float64(dy)+0.5)*float64(sh)/float64(dh) - 0.5
		if sy < 0 {
			sy = 0
		}
		if sy > float64(sh-1) {
			sy = float64(sh - 1)
		}
		y0 := int(sy)
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		wy := sy - float64(y0)
		for dx := 0; dx < dw; dx++ {
			sx := (float64(dx)+0.5)*float64(sw)/float64(dw) - 0.5
			if sx < 0 {
				sx = 0
			}
			if sx > float64(sw-1) {
				sx = float64(sw - 1)
			}
			x0 := int(sx)
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			wx := sx - float64(x0)

			c00 := srcNRGBA.NRGBAAt(x0, y0)
			c10 := srcNRGBA.NRGBAAt(x1, y0)
			c01 := srcNRGBA.NRGBAAt(x0, y1)
			c11 := srcNRGBA.NRGBAAt(x1, y1)

			interp := func(v00, v10, v01, v11 uint8) uint8 {
				top := float64(v00)*(1-wx) + float64(v10)*wx
				bot := float64(v01)*(1-wx) + float64(v11)*wx
				val := top*(1-wy) + bot*wy + 0.5
				if val < 0 {
					return 0
				}
				if val > 255 {
					return 255
				}
				return uint8(val)
			}

			r := interp(c00.R, c10.R, c01.R, c11.R)
			g := interp(c00.G, c10.G, c01.G, c11.G)
			b := interp(c00.B, c10.B, c01.B, c11.B)
			a := interp(c00.A, c10.A, c01.A, c11.A)
			dst.Pix[(offY+dy)*dst.Stride+(offX+dx)*4+0] = r
			dst.Pix[(offY+dy)*dst.Stride+(offX+dx)*4+1] = g
			dst.Pix[(offY+dy)*dst.Stride+(offX+dx)*4+2] = b
			dst.Pix[(offY+dy)*dst.Stride+(offX+dx)*4+3] = a
		}
	}
	return dst
}

func writePNGImage(img image.Image, destination string) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return writeAtomic(destination, buf.Bytes(), 0o644)
}

func installIcons(candidates []iconCandidate, dataHome, id string) []string {
	var installed []string
	installedSlots := make(map[string]bool)
	var largestPNG image.Image
	largestDim := 0

	for _, candidate := range candidates {
		if candidate.ext == ".svg" {
			iconDir := filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps")
			if err := os.MkdirAll(iconDir, 0o755); err != nil {
				continue
			}
			destination := filepath.Join(iconDir, id+".svg")
			if err := copyFileAtomic(candidate.path, destination, 0o644); err == nil {
				installed = append(installed, destination)
				installedSlots["scalable.svg"] = true
			}
			continue
		}
		if candidate.ext == ".png" {
			f, err := os.Open(candidate.path)
			if err == nil {
				img, _, decErr := image.Decode(io.LimitReader(f, 16<<20))
				_ = f.Close()
				if decErr == nil {
					b := img.Bounds()
					w, h := b.Dx(), b.Dy()
					dim := maxInt(w, h)
					if dim > largestDim {
						largestDim = dim
						largestPNG = img
					}
					if isStandardHicolorSize(w, h) {
						sizeDir := strconv.Itoa(w) + "x" + strconv.Itoa(h)
						slot := sizeDir + ".png"
						if !installedSlots[slot] {
							iconDir := filepath.Join(dataHome, "icons", "hicolor", sizeDir, "apps")
							if err := os.MkdirAll(iconDir, 0o755); err == nil {
								destination := filepath.Join(iconDir, id+".png")
								if err := copyFileAtomic(candidate.path, destination, 0o644); err == nil {
									installed = append(installed, destination)
									installedSlots[slot] = true
								}
							}
						}
						continue
					}
					target := nearestHicolorSize(dim)
					sizeDir := strconv.Itoa(target) + "x" + strconv.Itoa(target)
					slot := sizeDir + ".png"
					if !installedSlots[slot] {
						iconDir := filepath.Join(dataHome, "icons", "hicolor", sizeDir, "apps")
						if err := os.MkdirAll(iconDir, 0o755); err == nil {
							destination := filepath.Join(iconDir, id+".png")
							if err := writePNGImage(resizeImageNRGBA(img, target), destination); err == nil {
								installed = append(installed, destination)
								installedSlots[slot] = true
							}
						}
					}
					continue
				}
			}
		}
		iconDir := filepath.Join(dataHome, "icons", "hicolor", candidate.sizeDir, "apps")
		if err := os.MkdirAll(iconDir, 0o755); err != nil {
			continue
		}
		destination := filepath.Join(iconDir, id+candidate.ext)
		if err := copyFileAtomic(candidate.path, destination, 0o644); err == nil {
			installed = append(installed, destination)
		}
	}

	if largestPNG != nil {
		for _, sz := range cosmicRecommendedPNGSizes {
			if largestDim < sz {
				continue
			}
			sizeDir := strconv.Itoa(sz) + "x" + strconv.Itoa(sz)
			slot := sizeDir + ".png"
			if installedSlots[slot] {
				continue
			}
			iconDir := filepath.Join(dataHome, "icons", "hicolor", sizeDir, "apps")
			if err := os.MkdirAll(iconDir, 0o755); err != nil {
				continue
			}
			destination := filepath.Join(iconDir, id+".png")
			if err := writePNGImage(resizeImageNRGBA(largestPNG, sz), destination); err == nil {
				installed = append(installed, destination)
				installedSlots[slot] = true
			}
		}
	}

	return installed
}

func supportedIconExtension(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	header := make([]byte, 4096)
	n, _ := f.Read(header)
	header = header[:n]
	if len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n" {
		return ".png"
	}
	text := strings.TrimSpace(string(header))
	if strings.Contains(strings.ToLower(text), "<svg") {
		return ".svg"
	}
	if strings.Contains(text, "XPM") {
		return ".xpm"
	}
	return ""
}

func iconBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	base := filepath.Base(value)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func normalizedIconName(value string) string {
	return strings.ReplaceAll(slugify(value), "-", "")
}

func iconFormatScore(ext string) int64 {
	switch ext {
	case ".svg":
		return 3 << 34
	case ".png":
		return 2 << 34
	case ".xpm":
		return 1 << 34
	default:
		return 0
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func iconSizeDir(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".svg") || (filepath.Ext(path) == "" && supportedIconExtension(path) == ".svg") {
		return "scalable"
	}
	f, err := os.Open(path)
	if err != nil {
		return "512x512"
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(io.LimitReader(f, 16<<20))
	if err != nil || cfg.Width < 16 || cfg.Height < 16 {
		return "512x512"
	}
	s := nearestHicolorSize(maxInt(cfg.Width, cfg.Height))
	return strconv.Itoa(s) + "x" + strconv.Itoa(s)
}

func installedDesktopEntry(m desktopMetadata, executable, iconName, source string) string {
	wm := ""
	if m.StartupWMClass != "" {
		wm = "StartupWMClass=" + cleanDesktopValue(m.StartupWMClass) + "\n"
	}
	appVersion := ""
	if m.Version != "" {
		appVersion = "X-AppImage-Version=" + cleanAppVersion(m.Version) + "\n"
	}
	return fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=%s
Comment=%s
Exec=%s %%U
TryExec=%s
Icon=%s
Terminal=false
StartupNotify=true
%sCategories=%s
X-AppImage-Source=%s
%sX-AppImage-Integrator-Version=%s
`, cleanDesktopValue(m.Name), cleanDesktopValue(m.Comment), quoteExec(executable), cleanDesktopValue(executable), cleanDesktopValue(iconName), wm, cleanCategories(m.Categories), cleanDesktopValue(source), appVersion, version)
}

func safeDesktopID(value string) string {
	if len(value) < len("a.desktop") || len(value) > 200 || filepath.Base(value) != value || strings.HasPrefix(value, ".") {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(value), ".desktop") {
		return ""
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return ""
		}
	}
	if value == handlerID || value == launcherID {
		return ""
	}
	return value
}

func preferredDesktopEntry(appDir, embeddedID, fallbackID, source string) string {
	filename := safeDesktopID(embeddedID)
	if filename == "" {
		filename = fallbackID + ".desktop"
	}
	preferred := filepath.Join(appDir, filename)
	data, err := os.ReadFile(preferred)
	if errors.Is(err, os.ErrNotExist) || (err == nil && desktopEntryBelongsTo(data, source)) {
		return preferred
	}
	return filepath.Join(appDir, fallbackID+".desktop")
}

func desktopEntryBelongsTo(data []byte, source string) bool {
	needle := "X-AppImage-Source=" + cleanDesktopValue(source)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}

func cleanupPreviousIntegration(previous, current appMetadata, p paths) {
	if previous.ID != current.ID {
		return
	}
	if previous.Installed != "" && previous.Installed != current.Installed && isWithin(previous.Installed, filepath.Join(p.appsRoot, previous.ID)) {
		_ = os.Remove(previous.Installed)
	}
	if previous.DesktopFile != "" && previous.DesktopFile != current.DesktopFile && isWithin(previous.DesktopFile, p.appEntries) {
		_ = os.Remove(previous.DesktopFile)
	}
	if p.desktopDir != "" && previous.Name != current.Name {
		_ = os.Remove(filepath.Join(p.desktopDir, safeDesktopFilename(previous.Name)+".desktop"))
	}
	oldIcons := previous.Icons
	if len(oldIcons) == 0 && previous.Icon != "" {
		oldIcons = []string{previous.Icon}
	}
	currentIcons := make(map[string]bool, len(current.Icons))
	for _, path := range current.Icons {
		currentIcons[path] = true
	}
	for _, path := range oldIcons {
		if !currentIcons[path] && isWithin(path, filepath.Join(p.dataHome, "icons")) {
			_ = os.Remove(path)
		}
	}
}

func setupCommand(args []string) error {
	set := flag.NewFlagSet("setup", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	user := set.Bool("user", false, "per-user installation")
	system := set.Bool("system", false, "system-wide installation")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *user == *system {
		return errors.New("choose exactly one of --user or --system")
	}
	if *system {
		if os.Geteuid() != 0 {
			return errors.New("system-wide installation must be run with sudo")
		}
		return setupSystem()
	}
	if os.Geteuid() == 0 {
		return errors.New("do not use sudo with --user")
	}
	return setupUser()
}

func setupUser() error {
	p, err := userPaths()
	if err != nil {
		return err
	}
	binDir := filepath.Join(p.home, ".local", "bin")
	bin := filepath.Join(binDir, programName)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := installSelf(bin); err != nil {
		return err
	}
	if err := writeHandlerFiles(bin, p.dataHome); err != nil {
		return err
	}
	if err := setMIMEAssociation(filepath.Join(p.configHome, "mimeapps.list")); err != nil {
		return err
	}
	refreshMIME(filepath.Join(p.dataHome, "mime"), p.appEntries)
	_, _ = repairInstalledIcons(p)
	fmt.Println("AppImage handler installed for", p.home)
	fmt.Println("Double-clicking an AppImage will now integrate it into the application menu.")
	return nil
}

func setupSystem() error {
	bin := "/usr/local/bin/" + programName
	if err := installSelf(bin); err != nil {
		return err
	}
	if err := writeHandlerFiles(bin, "/usr/share"); err != nil {
		return err
	}
	if err := setMIMEAssociation("/etc/xdg/mimeapps.list"); err != nil {
		return err
	}
	refreshMIME("/usr/share/mime", "/usr/share/applications")
	fmt.Println("AppImage handler installed for all users.")
	return nil
}

func installSelf(destination string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	if sameFile(self, destination) {
		return os.Chmod(destination, 0o755)
	}
	return copyFileAtomic(self, destination, 0o755)
}

func writeHandlerFiles(binary, dataRoot string) error {
	apps := filepath.Join(dataRoot, "applications")
	mimePackages := filepath.Join(dataRoot, "mime", "packages")
	iconDir := filepath.Join(dataRoot, "icons", "hicolor", "512x512", "apps")
	metainfoDir := filepath.Join(dataRoot, "metainfo")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(mimePackages, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(metainfoDir, 0o755); err != nil {
		return err
	}
	iconPath := filepath.Join(iconDir, "auremi.png")
	if err := writeAtomic(iconPath, auremiLogo, 0o644); err != nil {
		return err
	}
	desktop := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Install an AppImage with Aurémi
Name[fr]=Installer une AppImage avec Aurémi
Comment=Integrate an AppImage into the Linux desktop with Aurémi
Comment[fr]=Intégrer une AppImage au bureau Linux avec Aurémi
Exec=%s integrate %%f
TryExec=%s
Icon=auremi
Terminal=false
NoDisplay=true
MimeType=application/vnd.appimage;application/x-iso9660-appimage;
`, quoteExec(binary), cleanDesktopValue(binary))
	if err := writeAtomic(filepath.Join(apps, handlerID), []byte(desktop), 0o644); err != nil {
		return err
	}
	launcher := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=Aurémi
Comment=Install, update, and uninstall integrated AppImages
Comment[fr]=Installer, mettre à jour et désinstaller les AppImage intégrées
Exec=%s manage
TryExec=%s
Icon=auremi
Terminal=false
StartupNotify=true
Categories=Utility;
Keywords=AppImage;Installer;Integration;Update;Uninstall;
Keywords[fr]=AppImage;Installation;Intégration;Mise à jour;Désinstallation;
`, quoteExec(binary), cleanDesktopValue(binary))
	if err := writeAtomic(filepath.Join(apps, launcherID), []byte(launcher), 0o644); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(metainfoDir, metainfoID), auremiMetainfo, 0o644); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(mimePackages, "appimage-integrator.xml"), []byte(mimeXML), 0o644)
}

func setMIMEAssociation(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := os.ReadFile(path)
	updated := updateINIList(string(data), "Default Applications", "application/vnd.appimage", handlerID)
	updated = updateINIList(updated, "Default Applications", "application/x-iso9660-appimage", handlerID)
	updated = updateINIList(updated, "Added Associations", "application/vnd.appimage", handlerID)
	updated = updateINIList(updated, "Added Associations", "application/x-iso9660-appimage", handlerID)
	return writeAtomic(path, []byte(updated), 0o644)
}

func updateINIList(content, section, key, value string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	header := "[" + section + "]"
	start, end := -1, len(lines)
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
					end = j
					break
				}
			}
			break
		}
	}
	entry := key + "=" + value + ";"
	if start == -1 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, header, entry)
		return strings.TrimLeft(strings.Join(lines, "\n"), "\n") + "\n"
	}
	for i := start + 1; i < end; i++ {
		k, old, ok := strings.Cut(lines[i], "=")
		if ok && strings.TrimSpace(k) == key {
			values := []string{value}
			for _, item := range strings.Split(old, ";") {
				item = strings.TrimSpace(item)
				if item != "" && item != value {
					values = append(values, item)
				}
			}
			lines[i] = key + "=" + strings.Join(values, ";") + ";"
			return strings.Join(lines, "\n")
		}
	}
	lines = append(lines[:end], append([]string{entry}, lines[end:]...)...)
	return strings.Join(lines, "\n")
}

func manageCommand() error {
	p, err := userPaths()
	if err != nil {
		return err
	}
	apps, err := managedApps(p)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		dialogMessage("No integrated AppImages were found.", false)
		return nil
	}
	selected, ok := selectManagedApplication(apps)
	if !ok {
		return nil
	}
	action, ok := dialogChoice("Manage "+selected.Name, "Choose an action", [][2]string{
		{"update", "Update from a new AppImage"},
		{"remove", "Uninstall"},
	})
	if !ok {
		return nil
	}
	if action == "remove" {
		return manageRemoval(selected)
	}
	return manageUpdate(selected, p)
}

func manageRemoval(m appMetadata) error {
	question := "Uninstall " + m.Name + "?\n\nIts managed AppImage, launcher, and icons will be removed.\nThe original download and application data will be preserved."
	if !dialogQuestion(question, "Uninstall", "Cancel") {
		return nil
	}
	if err := removeCommand([]string{m.ID}); err != nil {
		dialogMessage(err.Error(), true)
		return err
	}
	dialogMessage(m.Name+" was uninstalled.", false)
	return nil
}

func manageUpdate(m appMetadata, p paths) error {
	file, ok := selectAppImageFile()
	if !ok {
		return nil
	}
	result, err := integrateWithID(file, desktopShortcutExists(m, p), m.ID)
	if err != nil {
		dialogMessage("Update failed:\n"+err.Error(), true)
		return err
	}
	message := result.Name + " was updated successfully."
	if result.Version != "" {
		message += "\n\nVersion: " + result.Version
	}
	dialogMessage(message, false)
	return nil
}

func selectManagedApplication(apps []appMetadata) (appMetadata, bool) {
	choices := make([][2]string, 0, len(apps))
	byID := make(map[string]appMetadata, len(apps))
	for _, app := range apps {
		label := app.Name
		if app.Version != "" {
			label += "  " + app.Version
		}
		choices = append(choices, [2]string{app.ID, label})
		byID[app.ID] = app
	}
	id, ok := dialogChoice(fmt.Sprintf("Aurémi %s application manager", version), "Select an integrated application", choices)
	app, exists := byID[id]
	return app, ok && exists
}

func dialogChoice(title, message string, choices [][2]string) (string, bool) {
	icon := installedAuremiIcon()
	if path, err := exec.LookPath("kdialog"); err == nil {
		args := []string{"--title", title, "--icon", icon, "--menu", message}
		for _, choice := range choices {
			args = append(args, choice[0], choice[1])
		}
		return dialogOutput(path, args...)
	}
	if path, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--list", "--title=" + title, "--text=" + message, "--window-icon=" + icon, "--column=ID", "--column=Application", "--hide-column=1", "--print-column=1", "--width=640", "--height=420"}
		return dialogOutput(path, appendDialogRows(args, choices)...)
	}
	if path, err := exec.LookPath("yad"); err == nil {
		args := []string{"--list", "--title=" + title, "--text=" + message, "--window-icon=" + icon, "--column=ID:HD", "--column=Application", "--print-column=1", "--width=640", "--height=420"}
		return dialogOutput(path, appendDialogRows(args, choices)...)
	}
	fmt.Println(message)
	for _, choice := range choices {
		fmt.Printf("%s\t%s\n", choice[0], choice[1])
	}
	return "", false
}

func appendDialogRows(args []string, choices [][2]string) []string {
	for _, choice := range choices {
		args = append(args, choice[0], choice[1])
	}
	return args
}

func dialogOutput(path string, args ...string) (string, bool) {
	out, err := exec.Command(path, args...).Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(out))
	return value, value != ""
}

func selectAppImageFile() (string, bool) {
	home, _ := os.UserHomeDir()
	icon := installedAuremiIcon()
	if path, err := exec.LookPath("kdialog"); err == nil {
		return dialogOutput(path, "--title", "Select the new AppImage", "--icon", icon, "--getopenfilename", home, "*.AppImage *.appimage|AppImage applications")
	}
	if path, err := exec.LookPath("zenity"); err == nil {
		return dialogOutput(path, "--file-selection", "--title=Select the new AppImage", "--window-icon="+icon, "--file-filter=AppImage applications | *.AppImage *.appimage")
	}
	if path, err := exec.LookPath("yad"); err == nil {
		return dialogOutput(path, "--file-selection", "--title=Select the new AppImage", "--window-icon="+icon, "--file-filter=AppImage applications | *.AppImage *.appimage")
	}
	return "", false
}

func dialogQuestion(message, acceptLabel, cancelLabel string) bool {
	icon := installedAuremiIcon()
	if path, err := exec.LookPath("kdialog"); err == nil {
		return exec.Command(path, "--title", productName, "--icon", icon, "--yesno", message, "--yes-label", acceptLabel, "--no-label", cancelLabel).Run() == nil
	}
	if path, err := exec.LookPath("zenity"); err == nil {
		return exec.Command(path, "--question", "--title="+productName, "--window-icon="+icon, "--text="+message, "--ok-label="+acceptLabel, "--cancel-label="+cancelLabel).Run() == nil
	}
	if path, err := exec.LookPath("yad"); err == nil {
		return exec.Command(path, "--question", "--title="+productName, "--window-icon="+icon, "--text="+message, "--button="+acceptLabel+":0", "--button="+cancelLabel+":1").Run() == nil
	}
	if path, err := exec.LookPath("xmessage"); err == nil {
		return exec.Command(path, "-center", "-title", productName, "-buttons", acceptLabel+":0,"+cancelLabel+":1", message).Run() == nil
	}
	fmt.Println(message)
	return false
}

func dialogMessage(message string, failure bool) {
	icon := installedAuremiIcon()
	if path, err := exec.LookPath("kdialog"); err == nil {
		mode := "--msgbox"
		if failure {
			mode = "--error"
		}
		_ = exec.Command(path, "--title", productName, "--icon", icon, mode, message).Run()
		return
	}
	if path, err := exec.LookPath("zenity"); err == nil {
		mode := "--info"
		if failure {
			mode = "--error"
		}
		_ = exec.Command(path, mode, "--title="+productName, "--window-icon="+icon, "--text="+message).Run()
		return
	}
	if path, err := exec.LookPath("yad"); err == nil {
		mode := "--info"
		if failure {
			mode = "--error"
		}
		_ = exec.Command(path, mode, "--title="+productName, "--window-icon="+icon, "--text="+message, "--button=OK:0").Run()
		return
	}
	if path, err := exec.LookPath("xmessage"); err == nil {
		_ = exec.Command(path, "-center", "-title", productName, "-buttons", "OK:0", message).Run()
		return
	}
	fmt.Println(message)
}

func listCommand() error {
	p, err := userPaths()
	if err != nil {
		return err
	}
	apps, err := managedApps(p)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		fmt.Println("No integrated AppImages.")
		return nil
	}
	for _, m := range apps {
		appVersion := m.Version
		if appVersion == "" {
			appVersion = "unknown"
		}
		fmt.Printf("%-28s %-18s %s\n", m.ID, appVersion, m.Name)
	}
	return nil
}

func managedApps(p paths) ([]appMetadata, error) {
	entries, err := os.ReadDir(p.appsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps := make([]appMetadata, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.appsRoot, entry.Name(), "metadata.json"))
		var m appMetadata
		if err == nil && json.Unmarshal(data, &m) == nil && m.ID == entry.Name() {
			apps = append(apps, m)
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func managedAppByID(p paths, id string) (appMetadata, error) {
	if slugify(id) != id || id == "" {
		return appMetadata{}, errors.New("invalid ID")
	}
	appDir := filepath.Join(p.appsRoot, id)
	if !isWithin(appDir, p.appsRoot) {
		return appMetadata{}, errors.New("unsafe application path rejected")
	}
	data, err := os.ReadFile(filepath.Join(appDir, "metadata.json"))
	if err != nil {
		return appMetadata{}, errors.New("managed application not found")
	}
	var m appMetadata
	if err := json.Unmarshal(data, &m); err != nil || m.ID != id {
		return appMetadata{}, errors.New("invalid managed application metadata")
	}
	return m, nil
}

func desktopShortcutExists(m appMetadata, p paths) bool {
	if p.desktopDir == "" {
		return false
	}
	path := filepath.Join(p.desktopDir, safeDesktopFilename(m.Name)+".desktop")
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func removeCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("provide the ID displayed by the list command")
	}
	p, err := userPaths()
	if err != nil {
		return err
	}
	id := args[0]
	m, err := managedAppByID(p, id)
	if err != nil {
		return err
	}
	appDir := filepath.Join(p.appsRoot, id)
	if !isWithin(m.DesktopFile, p.appEntries) {
		return errors.New("invalid launcher path in metadata")
	}
	_ = os.Remove(m.DesktopFile)
	icons := m.Icons
	if len(icons) == 0 && m.Icon != "" {
		icons = []string{m.Icon}
	}
	for _, icon := range icons {
		if isWithin(icon, filepath.Join(p.dataHome, "icons")) {
			_ = os.Remove(icon)
		}
	}
	if p.desktopDir != "" {
		_ = os.Remove(filepath.Join(p.desktopDir, safeDesktopFilename(m.Name)+".desktop"))
	}
	if err := os.RemoveAll(appDir); err != nil {
		return err
	}
	refreshDesktop(p)
	fmt.Println(m.Name, "removed. The source file was not deleted.")
	return nil
}

func userPaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return paths{}, errors.New("home directory not found")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return paths{
		home:       home,
		dataHome:   dataHome,
		configHome: configHome,
		desktopDir: discoverDesktopDir(home, configHome),
		appsRoot:   filepath.Join(dataHome, programName, "apps"),
		appEntries: filepath.Join(dataHome, "applications"),
		iconsRoot:  filepath.Join(dataHome, "icons"),
	}, nil
}

func discoverDesktopDir(home, configHome string) string {
	data, _ := os.ReadFile(filepath.Join(configHome, "user-dirs.dirs"))
	re := regexp.MustCompile(`(?m)^XDG_DESKTOP_DIR="([^"]*)"`)
	if match := re.FindStringSubmatch(string(data)); len(match) == 2 {
		value := strings.ReplaceAll(match[1], "$HOME", home)
		if filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
	}
	for _, name := range []string{"Desktop", "Bureau"} {
		candidate := filepath.Join(home, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(home, "Desktop")
}

func uniqueID(base, source, root string) string {
	if base == "" {
		base = "appimage"
	}
	candidate := "appimage-" + base
	metaPath := filepath.Join(root, candidate, "metadata.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var m appMetadata
		if json.Unmarshal(data, &m) == nil && m.Source == source {
			return candidate
		}
	}
	if _, err := os.Stat(filepath.Join(root, candidate)); errors.Is(err, os.ErrNotExist) {
		return candidate
	}
	sum := sha256.Sum256([]byte(source))
	return candidate + "-" + hex.EncodeToString(sum[:3])
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func cleanDisplayName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\x00", ""), "_", " "))
	s = strings.TrimSuffix(s, ".AppImage")
	if len(s) > 100 {
		s = s[:100]
	}
	return cleanDesktopValue(s)
}

func cleanDesktopValue(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func cleanCategories(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ";") {
		part = regexp.MustCompile(`[^A-Za-z0-9-]`).ReplaceAllString(part, "")
		if part != "" && !seen[part] {
			out = append(out, part)
			seen[part] = true
		}
	}
	if len(out) == 0 {
		out = []string{"Utility"}
	}
	return strings.Join(out, ";") + ";"
}

func quoteExec(path string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$")
	return "\"" + replacer.Replace(path) + "\""
}

func safeAppImageFilename(name string) string {
	base := safeDesktopFilename(name)
	if base == "" {
		base = "Application"
	}
	return base + ".AppImage"
}

func safeDesktopFilename(name string) string {
	name = cleanDisplayName(name)
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, name)
	return strings.Trim(name, " .")
}

func copyFileAtomic(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func refreshDesktop(p paths) {
	hicolorDir := filepath.Join(p.dataHome, "icons", "hicolor")
	ensureHicolorIndexTheme(hicolorDir)
	now := time.Now()
	for _, dir := range []string{p.appEntries, p.iconsRoot, hicolorDir} {
		_ = os.Chtimes(dir, now, now)
	}
	runOptional("update-desktop-database", p.appEntries)
	runOptional("gtk-update-icon-cache", "-q", "-t", "-f", hicolorDir)
	runOptional("kbuildsycoca6", "--noincremental")
	refreshCosmicPanel()
}

func ensureHicolorIndexTheme(hicolorDir string) {
	target := filepath.Join(hicolorDir, "index.theme")
	if _, err := os.Stat(target); err == nil {
		return
	}
	if err := os.MkdirAll(hicolorDir, 0o755); err != nil {
		return
	}
	if data, err := os.ReadFile("/usr/share/icons/hicolor/index.theme"); err == nil && len(data) > 0 {
		_ = writeAtomic(target, data, 0o644)
		return
	}
	minimal := `[Icon Theme]
Name=Hicolor
Comment=Fallback icon theme
Directories=32x32/apps,48x48/apps,64x64/apps,128x128/apps,256x256/apps,512x512/apps,scalable/apps

[32x32/apps]
Size=32
Context=Applications
Type=Threshold

[48x48/apps]
Size=48
Context=Applications
Type=Threshold

[64x64/apps]
Size=64
Context=Applications
Type=Threshold

[128x128/apps]
Size=128
Context=Applications
Type=Threshold

[256x256/apps]
Size=256
Context=Applications
Type=Threshold

[512x512/apps]
Size=512
Context=Applications
Type=Threshold

[scalable/apps]
Size=48
MinSize=1
MaxSize=256
Context=Applications
Type=Scalable
`
	_ = writeAtomic(target, []byte(minimal), 0o644)
}

func isCosmicDesktop() bool {
	for _, key := range []string{"XDG_CURRENT_DESKTOP", "XDG_SESSION_DESKTOP", "DESKTOP_SESSION"} {
		if strings.Contains(strings.ToLower(os.Getenv(key)), "cosmic") {
			return true
		}
	}
	return false
}

func refreshCosmicPanel() {
	if !isCosmicDesktop() || os.Getenv("WAYLAND_DISPLAY") == "" || flag.Lookup("test.v") != nil {
		return
	}
	if _, err := exec.LookPath("pkill"); err != nil {
		return
	}
	// cosmic-app-list caches hicolor icon lookups in memory; restarting cosmic-panel
	// lets cosmic-session respawn it immediately with the new desktop entry and icons.
	runOptional("pkill", "-x", "cosmic-panel")
}

func repairCommand() error {
	p, err := userPaths()
	if err != nil {
		return err
	}
	repaired, err := repairInstalledIcons(p)
	if err != nil {
		return err
	}
	fmt.Printf("Repaired desktop icons for %d application(s).\n", repaired)
	return nil
}

// repairInstalledIcons normalizes existing icons into standard hicolor directories
// and switches desktop entries to themed icon names for COSMIC and other desktops.
func repairInstalledIcons(p paths) (int, error) {
	apps, err := managedApps(p)
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, app := range apps {
		changed := false
		var candidates []iconCandidate
		seen := make(map[string]bool)
		existing := append([]string{}, app.Icons...)
		if app.Icon != "" {
			existing = append(existing, app.Icon)
		}
		for _, path := range existing {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				continue
			}
			ext := supportedIconExtension(path)
			if ext == "" {
				continue
			}
			candidates = append(candidates, iconCandidate{
				path:    path,
				ext:     ext,
				sizeDir: iconSizeDir(path),
			})
		}

		if len(candidates) > 0 {
			newIcons := installIcons(candidates, p.dataHome, app.ID)
			if len(newIcons) > 0 {
				kept := make(map[string]bool, len(newIcons))
				for _, path := range newIcons {
					kept[path] = true
				}
				for _, oldPath := range existing {
					if !kept[oldPath] && isWithin(oldPath, p.iconsRoot) {
						_ = os.Remove(oldPath)
						changed = true
					}
				}
				if !sameStringSlice(app.Icons, newIcons) || app.Icon != newIcons[0] {
					app.Icons = newIcons
					app.Icon = newIcons[0]
					changed = true
				}
			}
		}

		if app.DesktopFile != "" {
			if updateDesktopIconLine(app.DesktopFile, app.ID) {
				changed = true
			}
		}
		if p.desktopDir != "" && app.Name != "" {
			shortcut := filepath.Join(p.desktopDir, safeDesktopFilename(app.Name)+".desktop")
			if _, err := os.Stat(shortcut); err == nil {
				if updateDesktopIconLine(shortcut, app.ID) {
					changed = true
				}
			}
		}
		if changed {
			encoded, _ := json.MarshalIndent(app, "", "  ")
			_ = writeAtomic(filepath.Join(p.appsRoot, app.ID, "metadata.json"), append(encoded, '\n'), 0o644)
			repaired++
		}
	}
	if repaired > 0 {
		refreshDesktop(p)
	}
	return repaired, nil
}

func updateDesktopIconLine(path, iconName string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	want := "Icon=" + cleanDesktopValue(iconName)
	changed := false
	for i, line := range lines {
		if strings.HasPrefix(line, "Icon=") {
			if line != want {
				lines[i] = want
				changed = true
			}
			break
		}
	}
	if !changed {
		return false
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")), 0o755) == nil
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func refreshMIME(mimeDir, appDir string) {
	runOptional("update-mime-database", mimeDir)
	runOptional("update-desktop-database", appDir)
}

func runOptional(name string, args ...string) {
	path, err := exec.LookPath(name)
	if err != nil {
		return
	}
	cmd := exec.Command(path, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}

func installedAuremiIcon() string {
	var candidates []string
	if p, err := userPaths(); err == nil {
		candidates = append(candidates, filepath.Join(p.dataHome, "icons", "hicolor", "512x512", "apps", "auremi.png"))
	}
	candidates = append(candidates, "/usr/share/icons/hicolor/512x512/apps/auremi.png")
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return "auremi"
}

func notify(title, body, icon string) {
	path, err := exec.LookPath("notify-send")
	if err != nil || os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return
	}
	args := []string{"--app-name", productName}
	if icon != "" {
		args = append(args, "--icon", icon)
	}
	args = append(args, title, body)
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

const mimeXML = `<?xml version="1.0" encoding="UTF-8"?>
<mime-info xmlns="http://www.freedesktop.org/standards/shared-mime-info">
  <mime-type type="application/vnd.appimage">
    <comment>AppImage application</comment>
    <glob pattern="*.AppImage" weight="90"/>
    <glob pattern="*.appimage" weight="90"/>
    <magic priority="80">
      <match type="string" offset="8" value="AI\x01"/>
      <match type="string" offset="8" value="AI\x02"/>
    </magic>
  </mime-type>
  <mime-type type="application/x-iso9660-appimage">
    <comment>AppImage application (compatibility)</comment>
    <sub-class-of type="application/vnd.appimage"/>
  </mime-type>
</mime-info>
`

const genericIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#7c3aed"/><stop offset="1" stop-color="#2563eb"/></linearGradient></defs>
  <rect width="512" height="512" rx="104" fill="url(#g)"/>
  <path d="M256 88 402 172v168L256 424 110 340V172z" fill="none" stroke="#fff" stroke-width="30" stroke-linejoin="round"/>
  <path d="M256 88v168m146-84-146 84-146-84m146 84v168" fill="none" stroke="#fff" stroke-width="24" stroke-linejoin="round" opacity=".9"/>
</svg>
`
