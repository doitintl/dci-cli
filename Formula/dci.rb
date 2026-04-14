class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.3.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.3.0/dci_1.3.0_darwin_arm64.tar.gz"
      sha256 "626d84fa7c6d83c97490227c38d0e180e5605d4651bb8b62ce843db89c81f2ed"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.3.0/dci_1.3.0_darwin_amd64.tar.gz"
      sha256 "8c96fd5c16fc4ae52e6d44d004574e1306b579015f5bd33ac3c7acae2b55cef5"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.3.0/dci_1.3.0_linux_arm64.tar.gz"
      sha256 "4a047acf2fb087c536905aee471408c5c4d1904aac4a957c4861e8dd7b70ea87"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.3.0/dci_1.3.0_linux_amd64.tar.gz"
      sha256 "914bd40f7c9e70815eb6916b2121f36efa33dad794a29cb5c6d54c854182fbe0"
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
