class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.4"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.4/dci_2.7.4_darwin_arm64.tar.gz"
      sha256 "37a2726935108de6745319adc1c0c47ae3a1b588d60c479ba8e4f534b36f6ca9"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.4/dci_2.7.4_darwin_amd64.tar.gz"
      sha256 "a38057c36339c113e17b406f2a1e17e446ccb8bf36b6eff0fae1a093b716dc41"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.4/dci_2.7.4_linux_arm64.tar.gz"
      sha256 "6a94c66205877286d3b62f67e6e38be9524503e64ac6c33da416fde904e235db"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.4/dci_2.7.4_linux_amd64.tar.gz"
      sha256 "9f4c0a6e1fc89dca0446dd35f0c1ff5a219a174c2a36bc378d1333747564c0f9"
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
