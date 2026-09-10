# Hidden setup: human mode regardless of the launching environment, and a
# minimal prompt that names the tenant.
Hide
Type "export DCI_AGENT_MODE=0; unset CLAUDECODE CLAUDE_CODE CI; PROMPT='%F{39}doit.com%f %F{245}❯%f '; clear"
Enter
Sleep 500ms
Show
