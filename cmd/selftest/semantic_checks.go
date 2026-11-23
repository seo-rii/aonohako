package main

import (
	"fmt"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func languageSemanticCases() map[string][]languageSemanticCase {
	cases := nonABSemanticCases()
	cases["plain"] = []languageSemanticCase{{
		name:           "text artifact is printed verbatim",
		compileLang:    "TEXT",
		expectedStdout: "text artifact content\n",
		sources:        []model.Source{{Name: "Main.txt", DataB64: encodeScript("text artifact content\n")}},
	}}
	cases["haskell"] = []languageSemanticCase{{
		name:           "Text and Map executable fits the artifact limit",
		stdin:          "지민\nalice\n지민\nbob\nend\n",
		expectedStdout: "3\nalice 2\nbob 4\n지민 3\n",
		sources: []model.Source{{Name: "Main.hs", DataB64: encodeScript(`{-# LANGUAGE OverloadedStrings #-}

import Prelude hiding (putStrLn, getLine)
import Data.Foldable (traverse_)
import Data.Map.Strict (empty, foldrWithKey, insert, Map(..), size)
import Data.Text (Text(..), pack)
import Data.Text.IO (putStrLn, getLine)

main :: IO ()
main = do
  result <- snd <$> indexNames (0, empty)
  print (size result)
  foldrWithKey (\name nth rest -> putStrLn (name <> " " <> pack (show nth)) >> rest) (pure ()) result

indexNames :: (Int, Map Text Int) -> IO (Int, Map Text Int)
indexNames (nth, mp) = do
  input <- getLine
  case input of
    "end" -> pure (nth, mp)
    _ -> let next = nth + 1
          in indexNames (next, insert input next mp)
`)}},
	}}
	cases["golfscript"] = []languageSemanticCase{
		{
			name:           "compact arithmetic parses signed input",
			stdin:          "-17\t9\n",
			expectedStdout: "-8\n",
			sources:        []model.Source{{Name: "Main.gs", DataB64: encodeScript("~+")}},
		},
		{
			name:              "addition without evaluating stdin underflows the stack",
			stdin:             "1 2\n",
			expectedRunStatus: model.RunStatusRE,
			sources:           []model.Source{{Name: "Main.gs", DataB64: encodeScript("+")}},
		},
	}
	cases["wren"] = []languageSemanticCase{
		{
			name: "workspace files are writable while submitted sources stay immutable",
			sources: []model.Source{{Name: "Main.wren", DataB64: encodeScript(`import "io" for File
File.create("same-folder.txt") {|file| file.writeBytes("ok") }
System.print(File.read("same-folder.txt"))
var overwrite = Fiber.new {
  File.create("Main.wren") {|file| file.writeBytes("changed") }
}
overwrite.try()
System.print(overwrite.error != null ? "blocked" : "modified")
`)}},
			expectedStdout: "ok\nblocked\n",
		},
		{
			name:              "syntax errors are rejected at execution",
			sources:           []model.Source{{Name: "Main.wren", DataB64: encodeScript("var broken =\n")}},
			expectedRunStatus: model.RunStatusRE,
		},
		{
			name: "fibers yield and resume",
			sources: []model.Source{{Name: "Main.wren", DataB64: encodeScript(`var fiber = Fiber.new {
  Fiber.yield(20)
  return 22
}
System.print(fiber.call() + fiber.call())
`)}},
			expectedStdout: "42\n",
		},
	}
	cases["nimony"] = []languageSemanticCase{{
		name:           "JSON values and a procedure annotation macro",
		expectedStdout: "42\n42\n",
		sources: []model.Source{{Name: "Main.nim", DataB64: encodeScript(`import std/[json, macros, syncio]

macro traced(p: untyped): untyped =
  result = p

proc work(x: int): int {.traced.} =
  x * 2

try:
  var tree = parseJson("""{"answer":42}""")
  echo getInt(tree{"answer"})
  echo work(21)
except:
  quit(1)
`)}},
	}}
	cases["scala3"] = []languageSemanticCase{
		{
			name:           "Scala 3 enum and extension syntax",
			expectedStdout: "42\n",
			sources: []model.Source{{Name: "Main.scala", DataB64: encodeScript(`enum Answer:
  case Value(value: Int)
extension (a: Answer)
  def number: Int = a match
    case Answer.Value(n) => n
object Main:
  def main(args: Array[String]): Unit =
    println(Answer.Value(42).number)
`)}},
		},
		{
			name:                  "Scala 3 type mismatch is rejected",
			expectedCompileStatus: model.CompileStatusCompileError,
			sources: []model.Source{{Name: "Main.scala", DataB64: encodeScript(`object Main:
  val incorrect: Int = "not an integer"
`)}},
		},
	}

	cases["picat"] = []languageSemanticCase{
		{
			name:              "failed main produces runtime error",
			expectedRunStatus: model.RunStatusRE,
			sources:           []model.Source{{Name: "Main.pi", DataB64: encodeScript("main => fail.\n")}},
		},
		{
			name:           "external command execution stays denied",
			expectedStdout: "blocked\n",
			sources: []model.Source{{Name: "Main.pi", DataB64: encodeScript(`main =>
    if command("true") == 0 then println("leaked") else println("blocked") end.
`)}},
		},
		{
			name:           "tabling and list comprehension",
			expectedStdout: "55\n[1,4,9,16]\n",
			sources: []model.Source{{Name: "Main.pi", DataB64: encodeScript(`table
fib(0) = 0.
fib(1) = 1.
fib(N) = F, N > 1 => F = fib(N-1)+fib(N-2).
main => println(fib(10)), println([X*X : X in 1..4]).
`)}},
		},
		{
			name:                  "reject invalid syntax before execution",
			expectedCompileStatus: model.CompileStatusCompileError,
			sources:               []model.Source{{Name: "Main.pi", DataB64: encodeScript("main => this is invalid.\n")}},
		},
	}
	cases["squirrel"] = []languageSemanticCase{
		{
			name:           "array iteration and arithmetic",
			expectedStdout: "30\n",
			sources:        []model.Source{{Name: "Main.nut", DataB64: encodeScript(`local values = [1,2,3,4]; local total=0; foreach(v in values) total+=v*v; print(total.tostring()+"\n");`)}},
		},
		{
			name:                  "invalid source rejected",
			expectedCompileStatus: model.CompileStatusCompileError,
			sources:               []model.Source{{Name: "Broken.nut", DataB64: encodeScript(`local value = ;`)}},
		},
	}
	cases["pari-gp"] = []languageSemanticCase{
		{
			name:           "integer factorization",
			expectedStdout: "[2, 3; 3, 2; 5, 1]\n",
			sources:        []model.Source{{Name: "Main.gp", DataB64: encodeScript(`print(factor(360));`)}},
		},
		{
			name:                  "invalid source rejected",
			expectedCompileStatus: model.CompileStatusCompileError,
			sources:               []model.Source{{Name: "Broken.gp", DataB64: encodeScript(`print(;`)}},
		},
	}
	return cases
}

func verifyLanguageSemanticCase(baseURL, language, compileLanguage string, baseline compileExecuteCase, tc languageSemanticCase) error {
	if tc.compileLang != "" {
		compileLanguage = tc.compileLang
	}
	profile, ok := profiles.Resolve(compileLanguage)
	if !ok {
		return fmt.Errorf("%s/%s %s: unknown compile profile", language, compileLanguage, tc.name)
	}
	compiled, err := postCompileRequest(baseURL, model.CompileRequest{
		Lang:       compileLanguage,
		Sources:    tc.sources,
		EntryPoint: baseline.entryPoint,
	})
	if err != nil {
		return fmt.Errorf("%s/%s %s compile request: %w", language, compileLanguage, tc.name, err)
	}
	wantCompileStatus := tc.expectedCompileStatus
	if wantCompileStatus == "" {
		wantCompileStatus = model.CompileStatusOK
	}
	if compiled.Status != wantCompileStatus {
		return fmt.Errorf("%s/%s %s compile status=%s, want %s: reason=%s stdout=%q stderr=%q", language, compileLanguage, tc.name, compiled.Status, wantCompileStatus, compiled.Reason, compiled.Stdout, compiled.Stderr)
	}
	if wantCompileStatus != model.CompileStatusOK {
		if len(compiled.Artifacts) != 0 {
			return fmt.Errorf("%s/%s %s rejected compile returned %d artifacts", language, compileLanguage, tc.name, len(compiled.Artifacts))
		}
		return nil
	}
	if len(compiled.Artifacts) == 0 {
		return fmt.Errorf("%s/%s %s compile succeeded without artifacts", language, compileLanguage, tc.name)
	}
	binaries := make([]model.Binary, 0, len(compiled.Artifacts))
	for _, artifact := range compiled.Artifacts {
		binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
	}
	limits := baseline.limits
	if limits.TimeMs <= 0 {
		limits.TimeMs = 6000
	}
	if limits.MemoryMB <= 0 {
		limits.MemoryMB = 512
	}
	request := model.RunRequest{
		Lang:           profile.RunLang,
		EntryMode:      baseline.entryMode,
		Binaries:       binaries,
		EntryPoint:     baseline.entryPoint,
		Stdin:          tc.stdin,
		ExpectedStdout: tc.expectedStdout,
		Limits:         limits,
	}
	result, err := postExecuteRequest(baseURL, request)
	if err != nil {
		return fmt.Errorf("%s/%s %s execute request: %w", language, compileLanguage, tc.name, err)
	}
	wantRunStatus := tc.expectedRunStatus
	if wantRunStatus == "" {
		wantRunStatus = model.RunStatusAccepted
	}
	if result.Status != wantRunStatus {
		return fmt.Errorf("%s/%s %s execute status=%s, want %s: reason=%s stdout=%q stderr=%q", language, compileLanguage, tc.name, result.Status, wantRunStatus, result.Reason, result.Stdout, result.Stderr)
	}
	if wantRunStatus == model.RunStatusAccepted {
		request.ExpectedStdout += "aonohako-wrong-answer-control\n"
		control, err := postExecuteRequest(baseURL, request)
		if err != nil {
			return fmt.Errorf("%s/%s %s wrong-answer control request: %w", language, compileLanguage, tc.name, err)
		}
		if control.Status != model.RunStatusWA {
			return fmt.Errorf("%s/%s %s wrong-answer control returned status=%s, want %s", language, compileLanguage, tc.name, control.Status, model.RunStatusWA)
		}
	}
	return nil
}
