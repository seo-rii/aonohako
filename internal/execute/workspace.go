package execute

import (
	"archive/zip"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/security"
	"aonohako/internal/util"
)

type Workspace struct {
	RootDir string
	BoxDir  string
}

func createRunWorkDir() (string, error) {
	return util.CreateWorkDir("aonohako-run-*")
}

func prepareWorkspaceDirs(workDir string) (Workspace, error) {
	ws := Workspace{
		RootDir: workDir,
		BoxDir:  filepath.Join(workDir, "box"),
	}
	dirs := security.WorkspaceScopedDirs(workDir)
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Workspace{}, err
		}
	}
	if err := os.MkdirAll(ws.BoxDir, 0o777); err != nil {
		return Workspace{}, err
	}
	if err := os.Chmod(ws.BoxDir, 0o777|os.ModeSticky); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

func materializeFiles(ws Workspace, req *model.RunRequest) (primaryPath string, lang string, err error) {
	lang = profiles.NormalizeRunLang(req.Lang)
	if lang == "" {
		lang = "binary"
	}
	var jarPath string
	var pyPath string
	var clojurePath string
	var rocqPath string
	var racketPath string
	var schemePath string
	var awkPath string
	var gdlPath string
	var octavePath string
	var verilogPath string
	var sourcePath string
	classFiles := make([]string, 0)
	entryPointPath := ""
	switch lang {
	case "java", "scala", "groovy":
		if _, err := normalizeJVMMainClass(req.EntryPoint, "Main"); err != nil {
			return "", "", err
		}
	case "erlang":
	default:
		if rawEntryPoint := strings.TrimSpace(req.EntryPoint); rawEntryPoint != "" {
			clean, err := util.ValidateRelativePath(rawEntryPoint)
			if err != nil {
				return "", "", fmt.Errorf("invalid entry_point: %w", err)
			}
			entryPointPath = clean
		}
	}
	submittedPaths := make(map[string]string, len(req.Binaries))
	submittedExec := make(map[string]bool, len(req.Binaries))
	totalBytes := 0
	for i, b := range req.Binaries {
		clean, err := util.ValidateRelativePath(b.Name)
		if err != nil {
			return "", "", err
		}
		data, err := base64.StdEncoding.DecodeString(b.DataB64)
		if err != nil {
			return "", "", fmt.Errorf("decode %s: %w", clean, err)
		}
		if len(data) > maxBinaryFileBytes {
			return "", "", fmt.Errorf("binary too large: %s", clean)
		}
		totalBytes += len(data)
		if totalBytes > maxBinaryTotalBytes {
			return "", "", fmt.Errorf("binaries total size exceeded")
		}
		dest := filepath.Join(ws.BoxDir, clean)
		parentDir := filepath.Dir(dest)
		if err := os.MkdirAll(parentDir, 0o777|os.ModeSticky); err != nil {
			return "", "", err
		}
		for dir := parentDir; dir != ws.BoxDir && strings.HasPrefix(dir, ws.BoxDir+string(os.PathSeparator)); dir = filepath.Dir(dir) {
			if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
				return "", "", err
			}
		}
		mode := os.FileMode(0o444)
		if b.Mode == "exec" || isLikelyExec(clean) {
			mode = 0o555
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return "", "", err
		}
		submittedPaths[clean] = dest
		submittedExec[clean] = mode&0o111 != 0
		if i == 0 {
			primaryPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".jar") {
			jarPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".py") && pyPath == "" {
			pyPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".clj") && clojurePath == "" {
			clojurePath = dest
		}
		lowerClean := strings.ToLower(clean)
		if strings.HasSuffix(lowerClean, ".v") && rocqPath == "" {
			rocqPath = dest
		}
		if strings.HasSuffix(lowerClean, ".rkt") && racketPath == "" {
			racketPath = dest
		}
		if strings.HasSuffix(lowerClean, ".scm") && schemePath == "" {
			schemePath = dest
		}
		if strings.HasSuffix(lowerClean, ".awk") && awkPath == "" {
			awkPath = dest
		}
		if strings.HasSuffix(lowerClean, ".pro") && gdlPath == "" {
			gdlPath = dest
		}
		if strings.HasSuffix(lowerClean, ".m") && octavePath == "" {
			octavePath = dest
		}
		if strings.HasSuffix(lowerClean, ".vvp") && verilogPath == "" {
			verilogPath = dest
		}
		for _, ext := range []string{".bas", ".carbon", ".graphql", ".lean", ".agda", ".dfy", ".tla", ".mlw", ".st", ".gs", ".ts", ".js", ".sql", ".bqn", ".apl", ".ua", ".janet"} {
			if strings.HasSuffix(lowerClean, ext) && sourcePath == "" {
				sourcePath = dest
				break
			}
		}
		if strings.HasSuffix(lowerClean, ".class") {
			classFiles = append(classFiles, clean)
		}
	}
	if entryPointPath != "" {
		selected, ok := submittedPaths[entryPointPath]
		if !ok {
			return "", "", fmt.Errorf("entry_point not found in binaries: %s", entryPointPath)
		}
		if lang == "binary" && !submittedExec[entryPointPath] {
			return "", "", fmt.Errorf("entry_point is not executable: %s", entryPointPath)
		}
		return selected, lang, nil
	}

	switch lang {
	case "binary", "javascript", "ruby", "php", "lua", "perl", "uhmlang", "csharp", "fsharp", "vbnet", "text", "ocaml", "elixir", "sqlite", "julia", "r", "prolog", "lisp", "whitespace", "brainfuck", "wasm", "aheui", "cuda-ocelot":
		return primaryPath, lang, nil
	case "python", "pypy":
		if pyPath == "" {
			pyPath = primaryPath
		}
		return pyPath, lang, nil
	case "clojure":
		if clojurePath == "" {
			clojurePath = primaryPath
		}
		return clojurePath, lang, nil
	case "racket":
		if racketPath == "" {
			racketPath = primaryPath
		}
		return racketPath, lang, nil
	case "scheme":
		if schemePath == "" {
			schemePath = primaryPath
		}
		return schemePath, lang, nil
	case "awk":
		if awkPath == "" {
			awkPath = primaryPath
		}
		return awkPath, lang, nil
	case "gdl":
		if gdlPath == "" {
			gdlPath = primaryPath
		}
		return gdlPath, lang, nil
	case "octave":
		if octavePath == "" {
			octavePath = primaryPath
		}
		return octavePath, lang, nil
	case "vhdl", "gleam", "isabelle":
		return ws.BoxDir, lang, nil
	case "verilog":
		if verilogPath == "" {
			verilogPath = primaryPath
		}
		return verilogPath, lang, nil
	case "erlang":
		hasBeam := false
		for _, binary := range req.Binaries {
			if strings.HasSuffix(strings.ToLower(binary.Name), ".beam") {
				hasBeam = true
				break
			}
		}
		if !hasBeam {
			return "", "", fmt.Errorf("erlang requires .beam files")
		}
		return ws.BoxDir, lang, nil
	case "java":
		if jarPath != "" {
			return jarPath, lang, nil
		}
		jar, err := buildSubmissionJar(ws.BoxDir, req.EntryPoint, classFiles)
		if err != nil {
			return "", "", err
		}
		return jar, lang, nil
	case "scala":
		if len(classFiles) == 0 {
			return "", "", fmt.Errorf("scala requires .class files")
		}
		return ws.BoxDir, lang, nil
	case "groovy":
		if len(classFiles) == 0 {
			return "", "", fmt.Errorf("groovy requires .class files")
		}
		return ws.BoxDir, lang, nil
	case "rocq":
		if rocqPath == "" {
			rocqPath = primaryPath
		}
		return rocqPath, lang, nil
	case "vb6", "carbon", "graphql", "lean4", "agda", "dafny", "tla", "why3", "smalltalk", "golfscript", "deno", "kotlin-jvm", "duckdb", "bqn", "apl", "uiua", "janet":
		if sourcePath == "" {
			sourcePath = primaryPath
		}
		return sourcePath, lang, nil
	default:
		return "", "", fmt.Errorf("unsupported run lang: %s", lang)
	}
}

func isLikelyExec(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".out") || strings.HasSuffix(l, ".bin") || strings.HasSuffix(l, ".run") || strings.HasSuffix(l, ".kexe") || (!strings.Contains(l, ".") && !strings.HasSuffix(l, "/"))
}

func buildSubmissionJar(workDir, entryPoint string, classes []string) (string, error) {
	if len(classes) == 0 {
		return "", fmt.Errorf("java requires .class files")
	}
	mainClass, err := normalizeJVMMainClass(entryPoint, "Main")
	if err != nil {
		return "", err
	}
	jarPath := filepath.Join(workDir, ".aonohako-submission.jar")
	file, err := os.Create(jarPath)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(file)
	mf, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		zw.Close()
		file.Close()
		return "", err
	}
	_, _ = mf.Write([]byte(fmt.Sprintf("Manifest-Version: 1.0\r\nMain-Class: %s\r\n\r\n", mainClass)))

	err = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".class") {
			rel, err := filepath.Rel(workDir, path)
			if err != nil {
				return err
			}
			entry, err := zw.Create(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = entry.Write(data)
			return err
		}
		return nil
	})
	if err != nil {
		zw.Close()
		file.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	_ = os.Chmod(jarPath, 0o444)
	return jarPath, nil
}

func normalizeJVMMainClass(raw, fallback string) (string, error) {
	mainClass := strings.TrimSpace(raw)
	if mainClass == "" {
		mainClass = fallback
	}
	mainClass = strings.ReplaceAll(mainClass, "/", ".")
	if mainClass == "" {
		return "", fmt.Errorf("invalid entry_point: empty JVM class")
	}
	for _, part := range strings.Split(mainClass, ".") {
		if part == "" {
			return "", fmt.Errorf("invalid entry_point: %q", raw)
		}
		for i := 0; i < len(part); i++ {
			ch := part[i]
			if i == 0 {
				if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' || ch == '$' {
					continue
				}
				return "", fmt.Errorf("invalid entry_point: %q", raw)
			}
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '$' {
				continue
			}
			return "", fmt.Errorf("invalid entry_point: %q", raw)
		}
	}
	return mainClass, nil
}
