# User

## User Information

(Important facts about user)
{{ if .ChildProfile.Name }}
- Name: {{ .ChildProfile.Name }}
{{ if .ChildProfile.Age }}- Age: {{ .ChildProfile.Age }} years old{{ end }}
{{ if .ChildProfile.Interests }}- Interests: {{ .ChildProfile.Interests }}{{ end }}
{{ if .ChildProfile.Gender }}- Gender: {{ .ChildProfile.Gender }}{{ end }}
{{ if .ChildProfile.Timezone }}- Timezone: {{ .ChildProfile.Timezone }}{{ end }}
{{ end }}
