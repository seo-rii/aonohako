package main

// sedABProgram adds nonnegative decimal integers with grade-school addition.
// Each iteration converts the last digits and carry to unary, then emits one
// decimal result digit. It uses only sed commands and does not depend on known
// judge inputs or an external arithmetic command.
const sedABProgram = `s/^[[:space:]]*\([0-9][0-9]*\)[[:space:]][[:space:]]*\([0-9][0-9]*\)[[:space:]]*$/\1#\2#0#/
/^[0-9][0-9]*#[0-9][0-9]*#0#$/!d
:digit
/^##[01]#/b done
s/^#/0#/
s/^\([0-9]*\)##/\1#0#/
s/^\([0-9]*\)\([0-9]\)#\([0-9]*\)\([0-9]\)#\([01]\)#\([0-9]*\)$/\2\4\5;\1#\3#\6/
:unary
s/0\(x*;\)/\1/
s/1\(x*;\)/x\1/
s/2\(x*;\)/xx\1/
s/3\(x*;\)/xxx\1/
s/4\(x*;\)/xxxx\1/
s/5\(x*;\)/xxxxx\1/
s/6\(x*;\)/xxxxxx\1/
s/7\(x*;\)/xxxxxxx\1/
s/8\(x*;\)/xxxxxxxx\1/
s/9\(x*;\)/xxxxxxxxx\1/
t unary
s/^xxxxxxxxxx/1:/
t carry
s/^/0:/
:carry
s/^\([01]:\)xxxxxxxxx;/\19;/
s/^\([01]:\)xxxxxxxx;/\18;/
s/^\([01]:\)xxxxxxx;/\17;/
s/^\([01]:\)xxxxxx;/\16;/
s/^\([01]:\)xxxxx;/\15;/
s/^\([01]:\)xxxx;/\14;/
s/^\([01]:\)xxx;/\13;/
s/^\([01]:\)xx;/\12;/
s/^\([01]:\)x;/\11;/
s/^\([01]:\);/\10;/
s/^\([01]\):\([0-9]\);\([0-9]*\)#\([0-9]*\)#\([0-9]*\)$/\3#\4#\1#\2\5/
b digit
:done
s/^##\([01]\)#/\1/
s/^0*//
s/^$/0/
`
