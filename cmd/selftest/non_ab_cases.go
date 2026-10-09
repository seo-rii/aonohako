package main

import "aonohako/internal/model"

// An A+B exception still needs to exercise the language's advertised behavior.
// Empty statuses use OK/Accepted so a successful execution is the default.
type languageSemanticCase struct {
	name                  string
	compileLang           string
	sources               []model.Source
	stdin                 string
	expectedStdout        string
	expectedCompileStatus string
	expectedRunStatus     string
}

func nonABSemanticCases() map[string][]languageSemanticCase {
	source := func(name, body string) model.Source {
		return model.Source{Name: name, DataB64: encodeScript(body)}
	}
	coqFalseProof := []model.Source{source("Main.v", `Theorem impossible : 1 = 2.
Proof. reflexivity. Qed.
`)}
	// The pinned APECode implementation exposes a rock-moving robot. Comparing
	// its two grippers exercises both branches and transforms an unsorted input.
	// Builtins: seo-rii/apecode, c7ae98d3dfc1713ecc800422a4c815628776e1e2,
	// src/apecode/cli.py (Robot.call).
	apeComparator := []model.Source{source("Main.ape", `state main {
  call pick_up_left;
  call move_right;
  call pick_up_right;
  call if_tilt_left;
  then {
    call put_down_left;
    call move_left;
    call put_down_right;
  } else {
    call put_down_right;
    call move_left;
    call put_down_left;
  }
  return true;
}
`)}
	// Inverse XLAT1 encoding for '/', '<', 'v' at positions 0, 1, 2:
	// read a byte, write it, halt. Ben Olmstead's original interpreter defines
	// these operations at https://www.lscheffer.com/malbolge_interp.html.
	malbolgeEcho := []model.Source{source("Main.mal", "ubO")}

	return map[string][]languageSemanticCase{
		"coq": {{
			name:                  "reject-false-equality-proof",
			sources:               coqFalseProof,
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"rocq": {{
			name:                  "reject-false-equality-proof",
			sources:               coqFalseProof,
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"lean4": {{
			name: "reject-false-equality-proof",
			sources: []model.Source{source("Main.lean", `theorem impossible : (1 : Nat) = 2 := by rfl
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"agda": {{
			name: "reject-type-mismatch",
			sources: []model.Source{source("Main.agda", `module Main where
data Unit : Set where
  tt : Unit
bad : Set
bad = tt
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"dafny": {{
			name: "reject-false-postcondition",
			sources: []model.Source{source("Main.dfy", `method Main() ensures false {
}
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"why3": {{
			name: "reject-false-goal",
			sources: []model.Source{source("Main.mlw", `theory Main
  goal Impossible: false
end
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"isabelle": {{
			name: "reject-false-theorem",
			sources: []model.Source{
				source("ROOT", "session Aonohako = HOL +\n  theories Aonohako_Main\n"),
				source("Aonohako_Main.thy", `theory Aonohako_Main
  imports Main
begin
theorem impossible: False by simp
end
`),
			},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"fstar": {{
			name: "reject-false-lemma",
			sources: []model.Source{source("Main.fst", `module Main
let impossible () : Lemma (1 + 1 == 3) = ()
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"alloy": {{
			name: "reject-assertion-counterexample",
			sources: []model.Source{source("Main.als", `sig A {}
assert Impossible { no A }
check Impossible for exactly 1 A
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"acl2": {{
			name: "reject-false-theorem",
			sources: []model.Source{source("Main.lisp", `(in-package "ACL2")
(defthm impossible (equal (+ 1 1) 3))
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"kframework": {{
			name: "reject-undefined-module",
			sources: []model.Source{source("Main.k", `module MAIN
  imports AONOHAKO_MISSING_MODULE
endmodule
`)},
			expectedCompileStatus: model.CompileStatusCompileError,
		}},
		"tla": {{
			name: "check-two-state-invariant",
			sources: []model.Source{
				source("Main.tla", `---- MODULE Main ----
VARIABLE x
Init == x = 0
Next == x' = IF x = 0 THEN 1 ELSE 0
Spec == Init /\ [][Next]_x
TypeOK == x \in {0, 1}
====
`),
				source("Main.cfg", "SPECIFICATION Spec\nINVARIANT TypeOK\n"),
			},
		}},
		"apecode": {
			{
				name:           "compare-and-swap-both-branches",
				sources:        apeComparator,
				stdin:          "2\n2\n9 2\n2\n1 7\n",
				expectedStdout: "2 9\n1 7\n",
			},
			{
				name:                  "reject-undefined-state",
				sources:               []model.Source{source("Main.ape", "state main { call missing; }\n")},
				expectedCompileStatus: model.CompileStatusCompileError,
			},
		},
		"malbolge": {
			{
				name:           "read-write-first-byte",
				sources:        malbolgeEcho,
				stdin:          "A\n",
				expectedStdout: "A",
			},
			{
				name:           "read-write-different-byte",
				sources:        malbolgeEcho,
				stdin:          "z\n",
				expectedStdout: "z",
			},
			{
				name:                  "reject-invalid-positional-opcode",
				sources:               []model.Source{source("Main.mal", "!!")},
				expectedCompileStatus: model.CompileStatusCompileError,
			},
		},
		"gdl": {{
			name: "array-arithmetic-and-reassignment",
			sources: []model.Source{source("Main.pro", `pro main
  values = [20, 22]
  print, values[0] + values[1], format='(I0)'
  values = [7, 13]
  print, values[0] + values[1], format='(I0)'
end
`)},
			expectedStdout: "42\n20\n",
		}},
		"graphql": {
			{
				name:           "select-and-alias-fields",
				sources:        []model.Source{source("Main.graphql", "query { sum: answer greeting: ok }\n")},
				expectedStdout: "{\"data\":{\"greeting\":\"ok\",\"sum\":42}}\n",
			},
			{
				name:              "reject-unknown-field",
				sources:           []model.Source{source("Main.graphql", "query { unknown }\n")},
				expectedRunStatus: model.RunStatusRE,
			},
		},
		"vb6": {
			{
				name: "literal-console-subset",
				sources: []model.Source{source("Main.bas", `Option Explicit
Sub Main()
' Comments must not become output.
Print "42"
Debug.Print "20"
End Sub
`)},
				expectedStdout: "42\n20\n",
			},
			{
				name:              "reject-unsupported-arithmetic",
				sources:           []model.Source{source("Main.bas", "Sub Main()\nPrint 20 + 22\nEnd Sub\n")},
				expectedRunStatus: model.RunStatusRE,
			},
		},
		"apl": {
			{
				name: "vector-reduction-and-reassignment",
				sources: []model.Source{source("Main.apl", `A←20 22
⎕←+/A
A←7 13
⎕←+/A
`)},
				expectedStdout: "42\n20\n",
			},
			{
				name:              "reject-undefined-variable",
				sources:           []model.Source{source("Main.apl", "⎕←UNBOUND\n")},
				expectedRunStatus: model.RunStatusRE,
			},
		},
		"zerolang": {{
			name: "constant-arithmetic-and-branches",
			// The pinned compiler's canonical source projection is demonstrated in
			// vercel-labs/zerolang v0.3.4/conformance/native/pass/const-arithmetic.0.
			sources: []model.Source{source("Main.0", `const first: i32 = 20 + 22
const second: i32 = 7 + 13

pub fn main(world: World) -> Void raises {
  if first == 42 {
    check world.out.write("42\n")
  } else {
    check world.out.write("arithmetic failed\n")
  }
  if second == 20 {
    check world.out.write("20\n")
  } else {
    check world.out.write("arithmetic failed\n")
  }
}
`)},
			expectedStdout: "42\n20\n",
		}},
	}
}
