Output out/01-ai-session.mp4
Source tapes/settings.tape
Source tapes/setup.tape

# Bare dci opens the AI session; Doers run keyless on DoiT-provided access.
Type "dci"
Sleep 800ms
Enter
Wait+Screen /commands ·/
Sleep 3s
Type "What are the top 3 cost anomalies this month?"
Sleep 1s
Enter
# The idle status hint is replaced by the spinner while the turn runs and
# comes back when the answer is complete.
Sleep 3s
Wait+Screen /for commands/
Sleep 6s
Type "/exit"
Sleep 500ms
Enter
Wait
Sleep 3s
