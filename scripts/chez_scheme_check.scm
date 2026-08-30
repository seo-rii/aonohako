(import (chezscheme))

(define arguments (command-line))
(unless (= (length arguments) 2)
  (error 'aonohako-chez-scheme-check "expected exactly one source path"))

(call-with-input-file (cadr arguments)
  (lambda (port)
    (let read-to-eof ()
      (unless (eof-object? (read port))
        (read-to-eof)))))
