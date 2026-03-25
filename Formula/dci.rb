class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.1.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.1/dci_1.1.1_darwin_arm64.tar.gz"
      sha256 "510fa00b973b0043a6d9f42c46749d5854c48a0b92141f47b05dc96b352b004b"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.1/dci_1.1.1_darwin_amd64.tar.gz"
      sha256 "574e02ce3ea1df91cd3cecbd183495ae81d22b3fa1aabe55c98295ef471490da"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.1/dci_1.1.1_linux_arm64.tar.gz"
      sha256 "5be06bb5aa32d6e68991f12817b871be2efef00a1525b612d7e1db6cc3b69bd7"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.1.1/dci_1.1.1_linux_amd64.tar.gz"
      sha256 "f707c17be1a6297cba18e160947ea2327dd4ae57a55770a66e714ae32b50fe61"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "DoiT Cloud Intelligence", output
  end
end
