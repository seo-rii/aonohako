package compile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"aonohako/internal/model"
)

func compileRocq(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	bin := "rocq"
	prefix := []string{"c"}
	if _, err := exec.LookPath("rocq"); err != nil {
		bin = "coqc"
		prefix = []string{"-q"}
	}
	return compileCheckedSources(ctx, workDir, sources, []string{".v"}, "no rocq sources", bin, prefix, []string{"OCAMLRUNPARAM=" + ocamlCompileRunParam})
}

func compileIsabelle(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	thyFiles := sourcePathsByExt(workDir, sources, ".thy")
	if len(thyFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no isabelle sources"}
	}
	if _, err := os.Stat(filepath.Join(workDir, "ROOT")); err != nil {
		theories := make([]string, 0, len(thyFiles))
		for _, path := range thyFiles {
			theories = append(theories, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
		sort.Strings(theories)
		root := "session Aonohako = HOL +\n  theories\n"
		for _, theory := range theories {
			root += "    " + theory + "\n"
		}
		if err := os.WriteFile(filepath.Join(workDir, "ROOT"), []byte(root), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	homeDir := filepath.Join(workDir, ".home")
	tmpDir := filepath.Join(workDir, ".tmp")
	isabelleSettingDirs := []string{
		filepath.Join(homeDir, ".isabelle", "etc"),
		filepath.Join(homeDir, ".isabelle", "Isabelle2025-2", "etc"),
	}
	if bin, err := exec.LookPath("isabelle"); err == nil {
		if realBin, err := filepath.EvalSymlinks(bin); err == nil {
			identifierPath := filepath.Clean(filepath.Join(filepath.Dir(realBin), "..", "etc", "ISABELLE_IDENTIFIER"))
			if data, err := os.ReadFile(identifierPath); err == nil {
				if identifier := strings.TrimSpace(string(data)); identifier != "" {
					isabelleSettingDirs = append(isabelleSettingDirs, filepath.Join(homeDir, ".isabelle", identifier, "etc"))
				}
			}
		}
	}
	isabelleSettings := fmt.Sprintf(
		"ISABELLE_TMP_PREFIX=\"%[1]s/isabelle\"\n"+
			"ISABELLE_SETUP_CLASSPATH=\"$ISABELLE_HOME/lib/classes/isabelle.jar:$ISABELLE_HOME/src/Tools/Demo/lib/demo.jar:$ISABELLE_HOME/lib/classes/isabelle_graphbrowser.jar\"\n"+
			"ISABELLE_SETUP_JAR=\"\"\n"+
			"ISABELLE_TOOL_JAVA_OPTIONS=\"-Djava.awt.headless=true -Djava.io.tmpdir=%[1]s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m\"\n"+
			"ISABELLE_JAVA_SYSTEM_OPTIONS=\"-server -Dfile.encoding=UTF-8 -Djava.io.tmpdir=%[1]s -Disabelle.threads=1 -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m\"\n"+
			"ISABELLE_JAVA_OPTIONS=\"-Djava.io.tmpdir=%[1]s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC\"\n",
		tmpDir,
	)
	seenSettings := map[string]struct{}{}
	for _, settingsDir := range isabelleSettingDirs {
		if _, ok := seenSettings[settingsDir]; ok {
			continue
		}
		seenSettings[settingsDir] = struct{}{}
		if err := os.MkdirAll(settingsDir, 0o777); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		for dir := settingsDir; strings.HasPrefix(dir, homeDir+string(os.PathSeparator)); dir = filepath.Dir(dir) {
			if err := os.Chmod(dir, 0o777); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			if dir == homeDir {
				break
			}
		}
		if err := os.WriteFile(filepath.Join(settingsDir, "settings"), []byte(isabelleSettings), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "isabelle", []string{"process_theories", "-o", "naproche_server=false", "-D", "."}, isabelleCompileEnv(workDir))
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		lower := strings.ToLower(name)
		return lower == "root" || strings.HasSuffix(lower, ".thy")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileOCaml(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	ordered := make([]string, 0, len(sources))
	hasML := false
	for _, src := range sources {
		name := strings.ToLower(src.Name)
		if strings.HasSuffix(name, ".ml") || strings.HasSuffix(name, ".mli") {
			ordered = append(ordered, filepath.Clean(src.Name))
		}
		if strings.HasSuffix(name, ".ml") {
			hasML = true
		}
	}
	if !hasML {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no ocaml sources"}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left := filepath.Base(ordered[i])
		right := filepath.Base(ordered[j])
		leftIsMain := strings.EqualFold(left, "Main.ml")
		rightIsMain := strings.EqualFold(right, "Main.ml")
		if leftIsMain != rightIsMain {
			return !leftIsMain
		}
		leftIsInterface := strings.HasSuffix(strings.ToLower(left), ".mli")
		rightIsInterface := strings.HasSuffix(strings.ToLower(right), ".mli")
		if leftIsInterface != rightIsInterface {
			return leftIsInterface
		}
		return left < right
	})
	args := []string{"-o", target}
	for _, rel := range ordered {
		args = append(args, filepath.Join(workDir, rel))
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "ocamlopt", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func isabelleCompileEnv(workDir string) []string {
	tmp := filepath.Join(workDir, ".tmp")
	return []string{
		fmt.Sprintf("JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=%s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m", tmp),
		"ISABELLE_JAVA_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_TOOL_JAVA_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_JAVA_SYSTEM_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_TMP_PREFIX=" + tmp,
	}
}
