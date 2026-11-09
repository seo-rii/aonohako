package main
import ("bufio"; "fmt"; "os")
func main() { var n int; fmt.Fscan(bufio.NewReader(os.Stdin), &n); x := uint32(1); for i := 0; i < n; i++ { x = x*1664525 + 1013904223 }; fmt.Println(x) }
