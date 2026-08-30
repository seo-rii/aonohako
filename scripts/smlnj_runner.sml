structure AonohakoSMLNJRunner =
struct
  val diagnosticLimit = 65536

  fun run (_, [sourcePath]) =
    let
      val originalPrinter = !Control.Print.out
      val diagnosticChunks = ref ([] : string list)
      val diagnosticBytes = ref 0

      fun capture chunk =
        let
          val remaining = diagnosticLimit - !diagnosticBytes
          val keep = Int.min (remaining, String.size chunk)
        in
          if keep <= 0 then
            ()
          else
            (diagnosticChunks := String.substring (chunk, 0, keep) :: !diagnosticChunks;
             diagnosticBytes := !diagnosticBytes + keep)
        end

      fun restorePrinter () = Control.Print.out := originalPrinter

      fun fail exn =
        let
          val diagnostic = String.concat (List.rev (!diagnosticChunks))
          val fallback = "SML/NJ execution failed: " ^ General.exnName exn ^ "\n"
        in
          restorePrinter ();
          TextIO.output (TextIO.stdErr, if diagnostic = "" then fallback else diagnostic);
          TextIO.flushOut TextIO.stdErr;
          OS.Process.failure
        end
    in
      Control.Print.out := {say = capture, flush = fn () => ()};
      ((use sourcePath;
        restorePrinter ();
        TextIO.flushOut TextIO.stdOut;
        OS.Process.success)
       handle exn => fail exn)
    end
    | run _ =
        (TextIO.output (TextIO.stdErr, "SML/NJ runner expects exactly one source path\n");
         TextIO.flushOut TextIO.stdErr;
         OS.Process.failure)
end

val _ = SMLofNJ.exportFn ("/usr/local/lib/aonohako/smlnj-run", AonohakoSMLNJRunner.run)
