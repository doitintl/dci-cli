class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.1/dci_2.7.1_darwin_arm64.tar.gz"
      sha256 "900f06d16640cc4717971d3d64ec1866e9338f75663038b44dd6bbe47b6396fc"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.1/dci_2.7.1_darwin_amd64.tar.gz"
      sha256 "f0c5f4a6d4e21ef90506871dcd76bc2e2dcf0a10b4a3c59caebac7ddba63f63f"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.1/dci_2.7.1_linux_arm64.tar.gz"
      sha256 "57f99812b9c158eb6efbdc5ce1c65fbf3f6227839d217305d762c76913a3704a"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.1/dci_2.7.1_linux_amd64.tar.gz"
      sha256 "5b7a8d2a66ae1ea7d5f7951b07f955989135949a116d82079a6ce9ca9aebe910"
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
