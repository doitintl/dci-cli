class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.8.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.8.0/dci_2.8.0_darwin_arm64.tar.gz"
      sha256 "3257e62d3cd56bd771475d603d90a1d308f8cbcb15e4293132c87b206b8d8d9b"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.8.0/dci_2.8.0_darwin_amd64.tar.gz"
      sha256 "dcbbb420ad59c9ef67e6a6c663207803ad9d8e1fd0965c90f7b9f10e69fc40bf"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.8.0/dci_2.8.0_linux_arm64.tar.gz"
      sha256 "abf521ab4802234c6681076b8917bd6f105c23aeb705e7854468dc030c2066a1"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.8.0/dci_2.8.0_linux_amd64.tar.gz"
      sha256 "a65b17a2ea59c8fd8282513f6f9ab4ed5115cae4e9e72ef9d902899d86d54662"
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
