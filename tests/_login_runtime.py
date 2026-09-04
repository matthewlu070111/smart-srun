"""Minimal runtime stub shared by default-login behavior tests."""


class StubLoginRuntime:
    def build_urls(self, base_url):
        return {
            "init_url": base_url,
            "get_challenge_api": base_url + "/cgi-bin/get_challenge",
            "srun_portal_api": base_url + "/cgi-bin/srun_portal",
            "rad_user_info_api": base_url + "/cgi-bin/rad_user_info",
            "rad_user_dm_api": base_url + "/cgi-bin/rad_user_dm",
        }

    def do_complex_work(self, cfg, ip, token):
        return "info", "hmd5", "chksum"
