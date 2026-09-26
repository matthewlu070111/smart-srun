include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-smart-srun
PKG_VERSION:=0.0.0
PKG_RELEASE:=1
PKG_LICENSE:=WTFPL
PKG_BUILD_DEPENDS:=golang1.27/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16
PKG_ASLR_PIE:=0

GO_PKG:=github.com/matthewlu070111/smart-srun/core
GO_PKG_BUILD_PKG:=$(GO_PKG)/cmd/srunnet
SMARTSRUN_DISPLAY_VERSION?=$(PKG_VERSION)
GO_PKG_LDFLAGS_X:=$(GO_PKG)/internal/cli.Version=$(SMARTSRUN_DISPLAY_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# Keep the SDK's architecture mapping and Go package lifecycle, but produce a
# static Go binary. The framework defaults to cgo and an external C linker.
GO_ARM64:=v8.0
GO_MIPS:=softfloat
# Set ABI baselines BEFORE := expands the framework's recursive target vars;
# otherwise cortex-a76 keeps v8.2 and hardware-FPU MIPS keeps hardfloat.
GO_PKG_TARGET_VARS:=$(filter-out CGO_ENABLED=1,$(GO_PKG_TARGET_VARS)) CGO_ENABLED=0
GO_PKG_DEFAULT_LDFLAGS:=-s -w -buildid '$(SOURCE_DATE_EPOCH)' -linkmode internal

# Keep the complete payload inside the targets.json budget without removing
# runtime features. MIPS needs all packages non-inlined; amd64 needs only
# application packages. The amd64 standard library and all other architectures
# retain normal inlining.
#
# D80 raised that budget from 10 to 16 MiB, which is what these -l flags were
# bought with: disabling inlining costs run-time speed on exactly the slowest
# devices. Restoring it is a separate change because it needs a measured build
# on the full SDK matrix, not an assumption that the headroom covers it.
ifneq ($(filter mips mipsle,$(GO_ARCH)),)
  GO_PKG_GCFLAGS:=all=-l
else ifeq ($(GO_ARCH),amd64)
  GO_PKG_GCFLAGS:=$(GO_PKG)/...=-l
endif

# Wireless observations use the iwinfo ubus object from rpcd-mod-iwinfo.
# Some forks ship the optional iwinfo CLI inside wifi-scripts instead.
RUNTIME_DEPENDS:=+ca-bundle +uci +ubus +procd +rpcd +rpcd-mod-iwinfo
LUCI_FILE_DEPENDS:=+luci-base +luci-compat

# core/ is the module root; no Python sources enter the build directory.
define Build/Prepare
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	$(CP) $(CURDIR)/core/go.mod $(CURDIR)/core/go.sum $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/core/cmd $(CURDIR)/core/internal $(PKG_BUILD_DIR)/
endef

define Package/smart-srun
  SECTION:=net
  CATEGORY:=Network
  TITLE:=SMART SRun campus network client
  DEPENDS:=$(RUNTIME_DEPENDS) $(GO_ARCH_DEPENDS)
  CONFLICTS:=$(if $(DUMP),,luci-app-smart-srun-bundle)
endef

define Package/smart-srun/description
  Go authentication daemon and CLI for SMART SRun campus networks.
endef

define Package/smart-srun/conffiles
/etc/smart-srun/config.json
/etc/smart-srun/user-presets.json
endef

define SmartSrun/InstallCore
	$(call GoPackage/Package/Install/Bin,$(1))
	$(INSTALL_DIR) $(1)/etc/smart-srun $(1)/etc/init.d
	chmod 0700 $(1)/etc/smart-srun
	$(INSTALL_BIN) $(CURDIR)/root/etc/init.d/smart_srun $(1)/etc/init.d/smart_srun
	$(INSTALL_BIN) $(CURDIR)/root/etc/init.d/smart_srun_update $(1)/etc/init.d/smart_srun_update
	$(INSTALL_DIR) $(1)/usr/share/smart-srun $(1)/lib/upgrade/keep.d
	$(INSTALL_DATA) $(CURDIR)/doc/school-presets.json $(1)/usr/share/smart-srun/school-presets.json
	$(INSTALL_DATA) $(CURDIR)/root/usr/share/smart-srun/third-party-licenses.txt $(1)/usr/share/smart-srun/third-party-licenses.txt
	$(INSTALL_DATA) $(CURDIR)/root/lib/upgrade/keep.d/smart-srun $(1)/lib/upgrade/keep.d/smart-srun
endef

define Package/smart-srun/install
	$(call SmartSrun/InstallCore,$(1))
endef

define Package/luci-app-smart-srun
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI interface for SMART SRun
  DEPENDS:=+smart-srun $(LUCI_FILE_DEPENDS)
  EXTRA_DEPENDS:=smart-srun (=$(VERSION))
  CONFLICTS:=$(if $(DUMP),,luci-app-smart-srun-bundle)
  PKGARCH:=all
endef

define Package/luci-app-smart-srun/description
  Frozen LuCI interface and local socket bridge for the SMART SRun Go daemon.
endef

define SmartSrun/InstallLuCI
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/controller $(1)/usr/lib/lua/luci/model/cbi
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/smart_srun $(1)/www/luci-static/resources
	$(INSTALL_DATA) $(CURDIR)/root/usr/lib/lua/luci/controller/smart_srun.lua $(1)/usr/lib/lua/luci/controller/smart_srun.lua
	$(INSTALL_DATA) $(CURDIR)/root/usr/lib/lua/luci/model/cbi/smart_srun.lua $(1)/usr/lib/lua/luci/model/cbi/smart_srun.lua
	$(INSTALL_DATA) $(CURDIR)/root/usr/lib/lua/luci/smart_srun/rpc.lua $(1)/usr/lib/lua/luci/smart_srun/rpc.lua
	$(INSTALL_DATA) $(CURDIR)/root/usr/lib/lua/luci/smart_srun/schema.lua $(1)/usr/lib/lua/luci/smart_srun/schema.lua
	$(INSTALL_DATA) $(CURDIR)/root/usr/lib/lua/luci/smart_srun/bridge.lua $(1)/usr/lib/lua/luci/smart_srun/bridge.lua
	$(INSTALL_DATA) $(CURDIR)/root/www/luci-static/resources/smart_srun.js $(1)/www/luci-static/resources/smart_srun.js
endef

define Package/luci-app-smart-srun/install
	$(call SmartSrun/InstallLuCI,$(1))
endef

define Package/luci-app-smart-srun-bundle
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=SMART SRun Go daemon and LuCI interface
  DEPENDS:=$(RUNTIME_DEPENDS) $(LUCI_FILE_DEPENDS) $(GO_ARCH_DEPENDS)
  CONFLICTS:=$(if $(DUMP),,smart-srun luci-app-smart-srun)
endef

define Package/luci-app-smart-srun-bundle/description
  Core and LuCI in one package; mutually exclusive with the split packages.
endef

define Package/luci-app-smart-srun-bundle/conffiles
$(Package/smart-srun/conffiles)
endef

define Package/luci-app-smart-srun-bundle/install
	$(call SmartSrun/InstallCore,$(1))
	$(call SmartSrun/InstallLuCI,$(1))
endef

# All three deliverables must build together. Runtime conflicts are omitted
# from the DUMP used to generate Kconfig: reciprocal conflicts plus LuCI's
# core dependency otherwise form a recursive Kconfig dependency.
$(eval $(call BuildPackage,smart-srun))
$(eval $(call BuildPackage,luci-app-smart-srun))
$(eval $(call BuildPackage,luci-app-smart-srun-bundle))

# APK needs negative runtime dependencies as well as IPK's CONFLICTS metadata.
ifneq ($(CONFIG_USE_APK),)
Package/smart-srun/DEPENDS += , !luci-app-smart-srun-bundle
Package/luci-app-smart-srun/DEPENDS += , !luci-app-smart-srun-bundle
Package/luci-app-smart-srun-bundle/DEPENDS += , !smart-srun, !luci-app-smart-srun
endif
