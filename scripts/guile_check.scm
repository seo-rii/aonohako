(define arguments (command-line))
(unless (= (length arguments) 2)
  (error "aonohako-guile-check expected exactly one source path"))

(call-with-input-file (cadr arguments)
  (lambda (port)
    (let read-to-eof ((form (read port)))
      (unless (eof-object? form)
        (read-to-eof (read port))))))
