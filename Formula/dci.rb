class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.6"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.6/dci_2.7.6_darwin_arm64.tar.gz"
      sha256 "cb50078cb17da718c6c621b7a538b92e230fab82c84a7360ed60cbc31bc66213"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.6/dci_2.7.6_darwin_amd64.tar.gz"
      sha256 "09d736b0e798864a2a276c56b5e3056cfcefdbcf2efcf4132e25df12587fdc8e"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.6/dci_2.7.6_linux_arm64.tar.gz"
      sha256 "9a671d1ce0c631738d225b65e43acee8d14c59098dc2e22cc7706259b36cfd37"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.6/dci_2.7.6_linux_amd64.tar.gz"
      sha256 "7869c2afa6da395c0e855b92e704294675ce25ad6ae52a7bbaf87a7f9dc2667b"
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
