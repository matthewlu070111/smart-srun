include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-smart-srun
PKG_VERSION:=0.0.0
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk

RUNTIME_DEPENDS:=+python3-light
LUCI_FILE_DEPENDS:=
LUCI_PACKAGE_DEPENDS:=+smart-srun $(LUCI_FILE_DEPENDS)
BUNDLE_DEPENDS:=$(RUNTIME_DEPENDS) $(LUCI_FILE_DEPENDS)

# ---------------------------------------------------------------------------
# Package: smart-srun (base CLI/TUI, no LuCI)
# ---------------------------------------------------------------------------

define Package/smart-srun
  SECTION:=net
  CATEGORY:=Network
  TITLE:=SMART SRun campus network client (CLI/TUI)
  DEPENDS:=$(RUNTIME_DEPENDS)
  PKGARCH:=all
endef

define Package/smart-srun/description
  Automatic SRun authentication daemon for SMART campus network.
  Includes CLI commands for status, login, logout, config, and log viewing.
endef

define Package/smart-srun/install
	$(INSTALL_DIR) $(1)/usr/lib/smart_srun
	$(INSTALL_DIR) $(1)/usr/lib/smart_srun/schools
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/*.py \
		$(1)/usr/lib/smart_srun/
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/defaults.json \
		$(1)/usr/lib/smart_srun/
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/schools/*.py \
		$(1)/usr/lib/smart_srun/schools/
	$(INSTALL_BIN) $(CURDIR)/root/etc/init.d/smart_srun \
		$(1)/etc/init.d/smart_srun
	$(if $(wildcard $(CURDIR)/root/etc/uci-defaults/*), \
		$(CP) $(CURDIR)/root/etc/uci-defaults/* \
			$(1)/etc/uci-defaults/)
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(CURDIR)/root/usr/bin/srunnet \
		$(1)/usr/bin/srunnet
endef

# ---------------------------------------------------------------------------
# Package: luci-app-smart-srun (LuCI addon, depends on smart-srun)
# ---------------------------------------------------------------------------

define Package/luci-app-smart-srun
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI interface for SMART SRun
  DEPENDS:=$(LUCI_PACKAGE_DEPENDS)
  CONFLICTS:=luci-app-smart-srun-bundle
  PKGARCH:=all
endef

define Package/luci-app-smart-srun/description
  LuCI web interface for the SMART SRun campus network client.
  Requires the smart-srun runtime package.
endef

define Package/luci-app-smart-srun/install
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/controller
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/model/cbi
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/smart_srun
	$(INSTALL_DIR) $(1)/www/luci-static/resources
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/controller/*.lua \
		$(1)/usr/lib/lua/luci/controller/
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/model/cbi/*.lua \
		$(1)/usr/lib/lua/luci/model/cbi/
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/smart_srun/*.lua \
		$(1)/usr/lib/lua/luci/smart_srun/
	$(CP) $(CURDIR)/root/www/luci-static/resources/*.js \
		$(1)/www/luci-static/resources/
endef

define Package/luci-app-smart-srun-bundle
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI interface for SMART SRun (bundle)
  DEPENDS:=$(BUNDLE_DEPENDS)
  CONFLICTS:=smart-srun luci-app-smart-srun
  PKGARCH:=all
endef

define Package/luci-app-smart-srun-bundle/description
  Self-contained package with both the SMART SRun runtime and LuCI files
  for manual installation without the split package pair.
endef

define Package/luci-app-smart-srun-bundle/install
	$(INSTALL_DIR) $(1)/usr/lib/smart_srun
	$(INSTALL_DIR) $(1)/usr/lib/smart_srun/schools
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/*.py \
		$(1)/usr/lib/smart_srun/
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/defaults.json \
		$(1)/usr/lib/smart_srun/
	$(CP) $(CURDIR)/root/usr/lib/smart_srun/schools/*.py \
		$(1)/usr/lib/smart_srun/schools/
	$(INSTALL_BIN) $(CURDIR)/root/etc/init.d/smart_srun \
		$(1)/etc/init.d/smart_srun
	$(if $(wildcard $(CURDIR)/root/etc/uci-defaults/*), \
		$(CP) $(CURDIR)/root/etc/uci-defaults/* \
			$(1)/etc/uci-defaults/)
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(CURDIR)/root/usr/bin/srunnet \
		$(1)/usr/bin/srunnet
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/controller
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/model/cbi
	$(INSTALL_DIR) $(1)/usr/lib/lua/luci/smart_srun
	$(INSTALL_DIR) $(1)/www/luci-static/resources
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/controller/*.lua \
		$(1)/usr/lib/lua/luci/controller/
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/model/cbi/*.lua \
		$(1)/usr/lib/lua/luci/model/cbi/
	$(CP) $(CURDIR)/root/usr/lib/lua/luci/smart_srun/*.lua \
		$(1)/usr/lib/lua/luci/smart_srun/
	$(CP) $(CURDIR)/root/www/luci-static/resources/*.js \
		$(1)/www/luci-static/resources/
endef

# ---------------------------------------------------------------------------
# Build (nothing to compile for pure-Python/Lua packages)
# ---------------------------------------------------------------------------

define Build/Compile
endef

$(eval $(call BuildPackage,smart-srun))
$(eval $(call BuildPackage,luci-app-smart-srun))
$(eval $(call BuildPackage,luci-app-smart-srun-bundle))
