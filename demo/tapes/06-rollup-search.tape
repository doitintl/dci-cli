Output out/06-rollup-search.mp4
Source tapes/settings.tape
Source tapes/setup.tape

Type 'dci get-report "CLI Demo - Top 10 Services (6 months)" --rollup service_description'
Sleep 800ms
Enter
Wait+Screen /service_description/
Wait
Sleep 4s

Type "clear"
Enter
Type "dci list-dimensions --search genai"
Sleep 800ms
Enter
Wait+Screen /system_label/
Wait
Sleep 3s
