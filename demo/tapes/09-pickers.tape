Output out/09-pickers.mp4
Source tapes/settings.tape
Source tapes/setup.tape

# Leave the name off and the picker opens; filter as you type.
Type "dci get-report-config"
Sleep 800ms
Enter
Wait+Screen /Select a report/
Sleep 1500ms
Type "CLI Demo"
Sleep 2s
Enter
# The config is ~40 lines, so a top-of-output key scrolls off; wait for the prompt.
Wait
Sleep 3s
