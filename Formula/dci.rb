class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.1.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.1/dci_2.1.1_darwin_arm64.tar.gz"
      sha256 "5eeba94bb2c4a0a468ab990a09ada0d9fb8331b39edb24da94aaadcf7b30d375"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.1/dci_2.1.1_darwin_amd64.tar.gz"
      sha256 "2e43fd405959f9369f2941dc997f230f51841d3ba2a3d9e4d3b4f8bfeeaf79b2"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.1/dci_2.1.1_linux_arm64.tar.gz"
      sha256 "61857ed1c7290a1e6c0e177a5b67d2cdd49f41d83be0a439c2516fa4015f7942"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.1/dci_2.1.1_linux_amd64.tar.gz"
      sha256 "a01989b5a87531006e72149fc6799b39e8a634f19efc28d80cde396d41622df8"
    end
  end

  def install
    bin.install "dci"
    bash_completion.install "completions/dci.bash" => "dci"
    zsh_completion.install "completions/dci.zsh" => "_dci"
    fish_completion.install "completions/dci.fish"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "Cloud Intelligence™", output
  end
end
