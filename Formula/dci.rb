class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.3.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.0/dci_2.3.0_darwin_arm64.tar.gz"
      sha256 "2b2222a8f9d8f3b97c471bfa1c7ce1983de067903d7e828c47e778d949fdb595"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.0/dci_2.3.0_darwin_amd64.tar.gz"
      sha256 "3d8f2cd3c3ea3490009d86291c41a1d1f44c121a5cd7b976f48c47881bcd9133"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.0/dci_2.3.0_linux_arm64.tar.gz"
      sha256 "316dd8aeb2191c64dc06e771f2ed13de30071438ac4654328ad4e845bdd24ed1"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.0/dci_2.3.0_linux_amd64.tar.gz"
      sha256 "f3c18b8a9b26267171d1f767bb49e2395b987fcef4031744b6b15f8bf80c6f8c"
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
