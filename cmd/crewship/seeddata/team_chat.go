package seeddata

import "embed"

// TeamChatPerson is a fictional colleague. JobTitle describes the demonstration;
// Role is an actual workspace RBAC role, not a new permission model.
type TeamChatPerson struct {
	Key, Email, FullName, Role, JobTitle, Message string
}

var TeamChatPeople = []TeamChatPerson{
	{Key: "thomas", Email: "thomas.team-demo@crewship.invalid", FullName: "Thomas · demo", Role: "ADMIN", JobTitle: "Platform administrator", Message: "Demo planning: I look after workspace access and platform setup. Let’s keep project updates in this channel."},
	{Key: "paul", Email: "paul.team-demo@crewship.invalid", FullName: "Paul · demo", Role: "MANAGER", JobTitle: "Product lead", Message: "Demo planning: I coordinate priorities. Peter and Sofia, let’s agree on acceptance criteria before starting the example task."},
	{Key: "peter", Email: "peter.team-demo@crewship.invalid", FullName: "Peter · demo", Role: "MEMBER", JobTitle: "Software engineer", Message: "Demo planning: I can take the implementation after Anna’s design review. This message is a fictional example, not a real assignment."},
	{Key: "anna", Email: "anna.team-demo@crewship.invalid", FullName: "Anna · demo", Role: "MANAGER", JobTitle: "Design lead", Message: "Demo planning: I’ll review the navigation and accessibility of the example screen with the team."},
	{Key: "sofia", Email: "sofia.team-demo@crewship.invalid", FullName: "Sofia · demo", Role: "MEMBER", JobTitle: "Quality engineer", Message: "Demo planning: I’ll check mobile behavior, safe retries and participant permissions for our example."},
	{Key: "emma", Email: "emma.team-demo@crewship.invalid", FullName: "Emma · demo", Role: "VIEWER", JobTitle: "Stakeholder", Message: "Demo planning: I follow the team’s progress and review the example outcome."},
}

//go:embed team-avatars/*.png
var teamChatAvatars embed.FS

// TeamChatAvatar returns a bundled local portrait, without an external image URL.
func TeamChatAvatar(key string) ([]byte, error) {
	return teamChatAvatars.ReadFile("team-avatars/" + key + ".png")
}
