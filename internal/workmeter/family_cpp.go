package workmeter

func init() {
	registerFamily(familySpec{ID: "cpp", CompileKinds: []string{"c", "cpp"}})
}
