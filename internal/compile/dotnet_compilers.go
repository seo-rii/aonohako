package compile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

type fsharpCompiler struct{}

func (fsharpCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileFSharp(ctx, job.WorkDir, job.Request.Sources)
}

func compileFSharp(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "fsproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var projectPath string
	var fsFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".fsproj") && projectPath == "" {
			projectPath = filepath.Join(projectDir, clean)
		}
		if strings.HasSuffix(lower, ".fs") {
			fsFiles = append(fsFiles, clean)
		}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if projectPath == "" {
		if len(fsFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no fsharp sources"}
		}
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		fsharpDir := filepath.Join(sdkDirs[len(sdkDirs)-1], "FSharp")
		fscPath := filepath.Join(fsharpDir, "fsc.dll")
		if _, err := os.Stat(fscPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "fsharp compiler not found"}
		}
		fsharpCorePath := filepath.Join(fsharpDir, "FSharp.Core.dll")
		if _, err := os.Stat(fsharpCorePath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "FSharp.Core not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDLLs, err := filepath.Glob(filepath.Join(refDirs[len(refDirs)-1], "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		args := []string{
			fscPath,
			"--nologo",
			"--target:exe",
			"--targetprofile:netcore",
			"--noframework",
			"--define:ONLINE_JUDGE",
			"--out:" + outDLL,
		}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		args = append(args, "-r:"+fsharpCorePath)
		for _, file := range fsFiles {
			args = append(args, filepath.Join(projectDir, file))
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json" || lower == "fsharp.core.dll"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	}
	outDir := filepath.Join(workDir, "publish")
	args := []string{"publish", projectPath, "--configuration", "Release", "-o", outDir, "-p:UseAppHost=false", "-p:DefineConstants=ONLINE_JUDGE", "-p:NuGetAudit=false"}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, dotnetBuildEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectDotnetPublishArtifacts(outDir, strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath)))
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type vbnetCompiler struct{}

func (vbnetCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileVBNet(ctx, job.WorkDir, job.Request.Sources)
}

func compileVBNet(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "vbproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var projectPath string
	var vbFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".vbproj") && projectPath == "" {
			projectPath = filepath.Join(projectDir, clean)
		}
		if strings.HasSuffix(lower, ".vb") {
			vbFiles = append(vbFiles, clean)
		}
	}
	if projectPath == "" && len(vbFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no vbnet sources"}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if projectPath == "" {
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		vbcPath := filepath.Join(sdkDirs[len(sdkDirs)-1], "Roslyn", "bincore", "vbc.dll")
		if _, err := os.Stat(vbcPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "vb compiler not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDir := refDirs[len(refDirs)-1]
		refDLLs, err := filepath.Glob(filepath.Join(refDir, "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		args := []string{vbcPath, "-nologo", "-nostdlib", "-sdkpath:" + refDir, "-vbruntime*", "-target:exe", "-optimize+", "-define:ONLINE_JUDGE=True", "-out:" + outDLL}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		for _, file := range vbFiles {
			args = append(args, filepath.Join(projectDir, file))
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	}
	outDir := filepath.Join(workDir, "publish")
	args := []string{"publish", projectPath, "--configuration", "Release", "-o", outDir, "-p:UseAppHost=false", "-p:DefineConstants=ONLINE_JUDGE", "-p:NuGetAudit=false"}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, dotnetBuildEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectDotnetPublishArtifacts(outDir, strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath)))
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type csharpCompiler struct{}

func (csharpCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCSharp(ctx, job.WorkDir, job.Request.Sources)
}

func compileCSharp(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "csproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var hasProject bool
	var projectPath string
	var csFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		if strings.HasSuffix(strings.ToLower(clean), ".cs") {
			csFiles = append(csFiles, filepath.Join(workDir, clean))
		}
		if strings.HasSuffix(strings.ToLower(clean), ".csproj") {
			hasProject = true
			if projectPath == "" {
				projectPath = filepath.Join(projectDir, clean)
			}
			break
		}
	}
	if !hasProject {
		if len(csFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no csharp sources"}
		}
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		cscPath := filepath.Join(sdkDirs[len(sdkDirs)-1], "Roslyn", "bincore", "csc.dll")
		if _, err := os.Stat(cscPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "csc compiler not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDLLs, err := filepath.Glob(filepath.Join(refDirs[len(refDirs)-1], "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		globalUsingsPath := filepath.Join(workDir, "Aonohako.GlobalUsings.g.cs")
		globalUsings := "global using System;\n" +
			"global using System.Collections.Generic;\n" +
			"global using System.IO;\n" +
			"global using System.Linq;\n" +
			"global using System.Net.Http;\n" +
			"global using System.Threading;\n" +
			"global using System.Threading.Tasks;\n"
		if err := os.WriteFile(globalUsingsPath, []byte(globalUsings), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		args := []string{cscPath, "-nologo", "-target:exe", "-langversion:latest", "-optimize+", "-define:ONLINE_JUDGE", "-out:" + outDLL}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		args = append(args, csFiles...)
		args = append(args, globalUsingsPath)
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	}
	if !hasProject {
		if _, _, status, reason := runCommand(ctx, workDir, "dotnet", []string{"new", "console", "--force", "-o", projectDir}, dotnetBuildEnv()); status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Reason: reason}
		}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	outDir := filepath.Join(workDir, "publish")
	publishTarget := projectDir
	if hasProject {
		publishTarget = projectPath
	}
	args := []string{"publish", publishTarget, "--configuration", "Release", "-o", outDir, "-p:UseAppHost=false", "-p:DefineConstants=ONLINE_JUDGE", "-p:NuGetAudit=false"}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, dotnetBuildEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	assemblyName := filepath.Base(projectDir)
	if hasProject {
		assemblyName = strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath))
	}
	artifacts, err := collectDotnetPublishArtifacts(outDir, assemblyName)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func dotnetRuntimeConfig() ([]byte, error) {
	runtimeDirs, err := filepath.Glob("/opt/dotnet/shared/Microsoft.NETCore.App/*")
	if err != nil || len(runtimeDirs) == 0 {
		return nil, fmt.Errorf("dotnet runtime not found")
	}
	sort.Strings(runtimeDirs)
	runtimeVersion := filepath.Base(runtimeDirs[len(runtimeDirs)-1])
	return []byte(fmt.Sprintf("{\n  \"runtimeOptions\": {\n    \"tfm\": \"net8.0\",\n    \"framework\": {\n      \"name\": \"Microsoft.NETCore.App\",\n      \"version\": %q\n    }\n  }\n}\n", runtimeVersion)), nil
}

func dotnetBuildEnv() []string {
	return []string{
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"DOTNET_NOLOGO=1",
		"MSBuildEnableWorkloadResolver=false",
	}
}

func collectDotnetPublishArtifacts(root, assemblyName string) ([]model.Artifact, error) {
	artifacts, err := collectArtifacts(root, func(name string) bool {
		l := strings.ToLower(name)
		return !strings.HasSuffix(l, ".pdb") && !strings.HasSuffix(l, ".xml")
	}, "publish")
	if err != nil {
		return nil, err
	}
	if assemblyName == "" {
		return artifacts, nil
	}
	mainDLL := "publish/" + assemblyName + ".dll"
	for i, artifact := range artifacts {
		if artifact.Name == mainDLL {
			if i != 0 {
				artifacts[0], artifacts[i] = artifacts[i], artifacts[0]
			}
			break
		}
	}
	return artifacts, nil
}
