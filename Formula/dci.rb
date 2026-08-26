class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.0/dci_2.7.0_darwin_arm64.tar.gz"
      sha256 "16a0dd9d2d2391ddb3f469a09f2a3425a2424e54687a3b20d636f17522f797a6"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.0/dci_2.7.0_darwin_amd64.tar.gz"
      sha256 "0a16e7090ce5dbfdaf25d155bea56107c763dc96faf1558342e4595c94eb3fd9"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.0/dci_2.7.0_linux_arm64.tar.gz"
      sha256 "fe5180b98cc67a3e87de809c23ea9f1c8aacb13981e49e1532ddbe2286d6406c"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.0/dci_2.7.0_linux_amd64.tar.gz"
      sha256 "e2ce079de20b01f187c9b00725d2c8ff493a9b7a442c13c815045b9d5824d626"
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
