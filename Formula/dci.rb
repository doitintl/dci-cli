class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "0.2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v0.2.0/dci_0.2.0_darwin_arm64.tar.gz"
      sha256 "6064734849b4a42aace39e6c307a414d985d6838b6a24e1b9d37dfecdf97a783"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v0.2.0/dci_0.2.0_darwin_amd64.tar.gz"
      sha256 "0dafb3977ab02f0bf0db2e0caf61edb747752012cb01f4c3d4f1a79283e0dcbe"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v0.2.0/dci_0.2.0_linux_arm64.tar.gz"
      sha256 "cdccc3ade925f12ef2fc5911ccef1e87f8146b196a5cdf2d395cf3bb6bfb786a"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v0.2.0/dci_0.2.0_linux_amd64.tar.gz"
      sha256 "2d061c88524fbd7b8e018815aee60bdfb7c7cac2bdf4849f81a0bc65fe0ff8db"
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
