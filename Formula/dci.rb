class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.5.2"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.2/dci_2.5.2_darwin_arm64.tar.gz"
      sha256 "34bd6332270daa9de6233fe5bd8fe11207b0b102ebe1649df09c12fbeb86f707"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.2/dci_2.5.2_darwin_amd64.tar.gz"
      sha256 "8b14876e79a600fadeb393d2051d28539377d8e39b0cdbd7d3e7beed542dabfb"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.2/dci_2.5.2_linux_arm64.tar.gz"
      sha256 "a55cf4230d1a3b59448f7e7e3ccec9175321b0a76897816ad38dc2c3775181e2"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.2/dci_2.5.2_linux_amd64.tar.gz"
      sha256 "13fec99b229c5f9c718a1c5081c810ab33d68dc9952e764d7b4b131e62cd37d6"
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
