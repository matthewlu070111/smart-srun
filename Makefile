include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-jxnu-srun
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/luci/luci.mk

LUCI_TITLE:=师大校园网
LUCI_DEPENDS:=+python3-light
LUCI_PKGARCH:=all

define Package/$(PKG_NAME)/description
 LuCI app for JXNU SRun campus network auto login.
endef

define Package/$(PKG_NAME)/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	chmod 0755 /etc/init.d/jxnu_srun 2>/dev/null
	chmod 0755 /usr/lib/jxnu_srun/client.py 2>/dev/null
}
exit 0
endef

# call BuildPackage - OpenWrt buildroot signature
