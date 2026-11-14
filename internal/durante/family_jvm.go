package durante

func init() {
	registerFamily(familySpec{
		ID: "jvm",
		SourceLanguages: []string{
			"JAVA",
			"JAVA8",
			"JAVA11",
			"JAVA15",
			"JAVA17",
			"JAVA21",
			"JAVA25",
			"GROOVY",
			"CLOJURE",
			"SCALA",
			"KOTLIN_JVM",
			"KOTLIN_JVM8",
			"KOTLIN_JVM11",
			"KOTLIN_JVM17",
			"KOTLIN_JVM21",
			"KOTLIN_JAVA",
			"KOTLIN_JAVA8",
			"KOTLIN_JAVA11",
			"KOTLIN_JAVA17",
			"KOTLIN_JAVA21",
		},
		RunLanguages: []string{"clojure", "groovy", "java", "java25", "kotlin-jvm", "scala"},
	})
}
