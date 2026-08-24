(local fennel (require :fennel))

(fn compiler-print [...]
  (each [index value (ipairs [...])]
    (when (> index 1)
      (io.stderr:write "\t"))
    (io.stderr:write (tostring value)))
  (io.stderr:write "\n")
  nil)

(local source-path (. arg 1))
(local output-path (. arg 2))
(assert (and source-path output-path) "usage: fennel_writer.fnl SOURCE OUTPUT")

(local source-file (assert (io.open source-path :rb)))
(local source (assert (source-file:read :*a)))
(source-file:close)

(local compiled
  (fennel.compile-string
    source
    {:filename source-path
     :requireAsInclude true
     :useMetadata false
     :extra-compiler-env {:print compiler-print}}))

(local output-file (assert (io.open output-path :wb)))
(assert (output-file:write compiled))
(assert (output-file:write "\n"))
(assert (output-file:close))
