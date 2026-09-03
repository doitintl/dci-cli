class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.2"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.2/dci_2.7.2_darwin_arm64.tar.gz"
      sha256 "472b68ae42e22040a7236df7e2d6d942052f15e3d7950d8e78862c76bdd78da1"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.2/dci_2.7.2_darwin_amd64.tar.gz"
      sha256 "c852e00214ac4863e76014fe792b9b4f61b296d2fb37bc0edd1e80ed08897724"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.2/dci_2.7.2_linux_arm64.tar.gz"
      sha256 "68ea819c6e8d1845ee86aebd91731883c1bee14f88bff958b22dadb841ecfb5b"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.2/dci_2.7.2_linux_amd64.tar.gz"
      sha256 "06117f9b884ed47c92239edced151ee857e73719806cb6dcddc42399e9d9570c"
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
