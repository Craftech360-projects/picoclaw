# User

Information about the user goes here.


## User Information

(Important facts about user)
{{ if .ChildProfile.Name }}
- Name: {{ .ChildProfile.Name }}
{{ if .ChildProfile.Age }}- Age: {{ .ChildProfile.Age }} years old{{ end }}
{{ if .ChildProfile.Interests }}- Interests: {{ .ChildProfile.Interests }}{{ end }}
{{ if .ChildProfile.Gender }}- Gender: {{ .ChildProfile.Gender }}{{ end }}
{{ if .ChildProfile.Timezone }}- Timezone: {{ .ChildProfile.Timezone }}{{ end }}
{{ end }}


## Preferences

- Communication style: (casual/formal)
- Timezone: (your timezone)
- Language: (your preferred language)

## Personal Information

- Name: (optional)
- Location: (optional)
- Occupation: (optional)

## Learning Goals

- What the user wants to learn from AI
- Preferred interaction style
- Areas of interest
