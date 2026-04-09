package seeddata

// CrewDef defines a crew to seed.
type CrewDef struct {
	Name string
	Slug string
	Color string
	Icon  string
}

var Crews = []CrewDef{
	{Name: "Engineering", Slug: "engineering", Color: "#3B82F6", Icon: "terminal"},
	{Name: "Quality", Slug: "quality", Color: "#10B981", Icon: "shield-check"},
	{Name: "DevOps", Slug: "devops", Color: "#EF4444", Icon: "server"},
	{Name: "Research", Slug: "research", Color: "#06B6D4", Icon: "telescope"},
}
