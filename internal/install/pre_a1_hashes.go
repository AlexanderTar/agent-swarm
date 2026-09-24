package install

// preA1SkillBodyHashes are the sha256 hex digests of every body
// skills/swarm/SKILL.md and skills/swarm-orchestrator/SKILL.md had on main as
// of 2026-09-24 (round 2, item 1: this is a frozen, point-in-time snapshot --
// it does NOT auto-track "the current body" and must not be regenerated just
// because either file changes again; a future body only needs adding here if
// an install written with that exact body needs to be recognized as pre-A1).
// isPreA1CoreSkillDir (review round 2, I2) treats a marker-less directory as
// swarm-owned -- safe to adopt during an explicit `swarm install` -- only
// when its SKILL.md byte-matches one of these, never merely because its
// frontmatter `name:` matches: a user's own same-named skill is
// astronomically unlikely to be byte-identical to something swarm actually
// shipped.
//
// Generated 2026-09-24 with:
//
//	for f in skills/swarm/SKILL.md skills/swarm-orchestrator/SKILL.md; do
//	  for c in $(git log --format=%H main -- "$f"); do
//	    git show "$c:$f" | shasum -a 256 | cut -d' ' -f1
//	  done
//	done | sort -u
var preA1SkillBodyHashes = map[string]bool{
	"0286d979d8918fc1dae580b279e127d0f98d6dfdb4eacca3ee9ba488f5ac0131": true,
	"077d0f095425ad598ceef14637bff564a33858b43ef8279b458fbe1e9e2c00f4": true,
	"0e2c2161f13a9be5b48ea188f0020d8551da86fff5b7f296ac4e6448e7f8cac5": true,
	"1523d6a6bde8a6a1fae27985cd5f2c8be72d5a1e5424baa530710fb63efdce62": true,
	"17baaaa1223bcf13ca01f04c24c6cdcb8f6d5e0349a0930efd506a2e58e9a688": true,
	"1f10dbc7a35a63558b6b6c366f341667d33c9ac15e793bc6b4166b079840e1c9": true,
	"2ae0b8d5bce3da1777c5165370c955a5857ba6a6029f06d737ce9de24c6d7a20": true,
	"3d0e41986d9b27fd4438e29715be799cb12df23f2bf5492985fd633f3a603b37": true,
	"3d1c2cd758f9a3b243e724c29b891e3294fb27a8a7c4c91a971fbe27ed4c744d": true,
	"3f6d34e2cd279fb852ccb4d7df93003ee2321d4abb03ff5130d6bfc177e9eb64": true,
	"43a275c7143478cbc82fe3db9b0f5349a71d28709903bc32b9ae4d1d7739ba03": true,
	"4c7e18e715ad2a4c11b64c397ca397636412ca085163a1b006adf484f5d89fbc": true,
	"5087eeda1edaf78b8c641c8cfc8a76d9b7ea29cee87a67f022072f6f0ff0eece": true,
	"5723d5c2e481e77a02f83714bea2d5c952f1efa30ba910ea66fc6a16f1b14b18": true,
	"5cfdebc458109e8641213981c65425253038b3b0131d9472967c7275baa1dc0f": true,
	"636554a90ad08be003719b42576335cc47066139a5aa5c96f20195a0796a31df": true,
	"6a146bfe8ece57b27c35c35876a90fbcbc3a5dd5ae3a169c7d415c81df209c4a": true,
	"745b65a325816d52e4a7c89e2f199b185b39b8363ac318f40895a8de0dfcd6a8": true,
	"77267c0f06566ef4d009ee3d76ee0207975cb20e9f87a1e4fe97f2b07feaf27c": true,
	"80c17a71bbe44d688b23c77ce65fc0c622515c8795b6305f3c896de692c13829": true,
	"8745780f7fd7f085536d6958f5207c45102c4fd5d82bf1c858acdee1227cb955": true,
	"8e5c503da42bd23302e2e00c6301b167fe320c5542a0d5b85c5222073e9db002": true,
	"9a50765795368cc45155e97ca2967382278092e3c300d209c9ce7350f4ae7911": true,
	"aa4aab01d14d8d551b6e8b3bb2960d6ea6c503887e54cda5bd06dc8f3b3b2dda": true,
	"ad3f74ddd41c36f6f572b685d59c642c61793ead85bfe8a86ab0b51683e43ec1": true,
	"ae10e76ac828a3ea4ea812df3d31a9be42c02979ab899f6e674106d8c06070f7": true,
	"c8bff758982c905f24f36dc10f3ff111a6da5f89a0353b5dda546787b37199c0": true,
	"cb9866c001d107934777ae6ab92e2cf3535d609b149885ee034c1e4ff5049df4": true,
	"cfe26829c1cf9be1b43bd860195799271af6e9b77d4a0b79de284e4deaed7868": true,
	"d3ba620338754a5aead608b70763d0507b40934d552d64854a53d864ede8762e": true,
	"d59b5625c63a75dbb36c4f3398cdf9e01141ce1e12f0eb0d4da320d1e540ea07": true,
	"d73c330b9436c4cd32c82f6a61851f15027a9c32f36bcf8c219753f7ea1ce444": true,
	"db2e8e6e5b535d3e8ed33273bafb4e96432355a189dc968068cb657a5b8330e5": true,
	"de9aa42461451640dcce13dae812c33cc6563ca70b7842a573c6257c7ca19e3a": true,
	"e4f2261e189d270b0d89c1d8c5be259a6349ccb89ad2ed8ad41a0e5c98a11fe2": true,
}
