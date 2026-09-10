Output out/10-update.mp4
Source tapes/settings.tape
# brew upgrade can take a few minutes.
Set WaitTimeout 900s
Source tapes/setup.tape

# record.sh records this section first, while the installed CLI is still the
# previous release, so the remaining sections run on the version being shown.
# Recording-shell only: skip Homebrew's tap-trust lecture about unrelated
# third-party taps on this machine, and its env hints.
Hide
Type "export HOMEBREW_NO_REQUIRE_TAP_TRUST=1 HOMEBREW_NO_ENV_HINTS=1; clear"
Enter
Sleep 300ms
Show

Type "dci --version"
Enter
Wait
Sleep 2s
Type "dci update"
Sleep 800ms
Enter
Wait+Screen /Proceed/
Sleep 2s
Type "y"
# Homebrew asks its own question before upgrading.
Wait+Screen /proceed with the upgrade/
Sleep 1s
Type "y"
Enter
Wait
Sleep 1s
Type "dci --version"
Enter
Wait
Sleep 3s
