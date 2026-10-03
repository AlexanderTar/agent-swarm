import Foundation

/// Repository picker rules (§16.3), the same as the board's `web/src/logic/repos.ts`.
public enum RepoPicker {
    public struct Section: Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
        /// Set for group sections, which get the [All] button.
        public var groupName: String?
        public var repos: [Repo]
    }

    /// "~/GitHub" for "/Users/alex/GitHub/endurio-chat".
    public static func parentFolder(_ path: String, home: String = NSHomeDirectory()) -> String {
        let dir = (path as NSString).deletingLastPathComponent
        if dir == home { return "~" }
        if dir.hasPrefix(home + "/") { return "~" + dir.dropFirst(home.count) }
        return dir.isEmpty ? "/" : dir
    }

    /// The shortened full path distinguishes repositories with the same name.
    public static func subtitle(_ r: Repo, home: String = NSHomeDirectory()) -> String {
        let path = (r.path as NSString).standardizingPath
        return path == home ? "~" : path.hasPrefix(home + "/") ? "~" + path.dropFirst(home.count) : path
    }

    /// The secondary line under a row, if any.
    public static func note(_ r: Repo) -> String? {
        if r.missing { return Copy.repoMissing }
        if r.dirty { return Copy.repoDirty }
        return nil
    }

    /// One present row per canonical path, sourced only from `all`.
    public static func rows(_ response: ReposResponse) -> [Repo] {
        var seen = Set<String>()
        return response.all.filter { repo in
            !repo.missing && seen.insert((repo.path as NSString).standardizingPath).inserted
        }.sorted {
            let byName = $0.name.localizedStandardCompare($1.name)
            return byName == .orderedSame
                ? $0.path.localizedStandardCompare($1.path) == .orderedAscending
                : byName == .orderedAscending
        }
    }

    /// Recent, then groups (folder-based first, then remote owners, each alphabetical;
    /// same-name groups merged), then All. Empty sections are dropped.
    public static func sections(_ r: ReposResponse) -> [Section] {
        var order: [String] = []
        var merged: [String: (local: Bool, repos: [Repo])] = [:]
        for g in r.groups {
            if merged[g.name] == nil { order.append(g.name) }
            var entry = merged[g.name] ?? (false, [])
            entry.local = entry.local || g.source != "remote_owner"
            for repo in g.repos where !entry.repos.contains(where: { $0.id == repo.id }) {
                entry.repos.append(repo)
            }
            merged[g.name] = entry
        }
        let groups = order.sorted { a, b in
            let la = merged[a]!.local, lb = merged[b]!.local
            return la != lb ? la : a.localizedCaseInsensitiveCompare(b) == .orderedAscending
        }
        var out: [Section] = []
        if !r.recent.isEmpty { out.append(Section(id: "recent", title: Copy.recent, groupName: nil, repos: r.recent)) }
        for name in groups {
            out.append(Section(id: "group:" + name, title: name, groupName: name, repos: merged[name]!.repos))
        }
        if !r.all.isEmpty { out.append(Section(id: "all", title: Copy.all, groupName: nil, repos: r.all)) }
        return out
    }

    public static func known(_ r: ReposResponse) -> [Repo] {
        var seen = Set<String>()
        return (r.recent + r.groups.flatMap(\.repos) + r.all).filter { seen.insert($0.id).inserted }
    }

    public static func toggle(_ selection: [String], _ id: String) -> [String] {
        selection.contains(id) ? selection.filter { $0 != id } : selection + [id]
    }

    /// A group's [All] adds every available repo in it.
    public static func selectAll(_ selection: [String], _ repos: [Repo]) -> [String] {
        selection + repos.filter { !$0.missing && !selection.contains($0.id) }.map(\.id)
    }

    /// "Scanning your home folder…", "Scanned 2h ago", or "Never scanned" before the first scan.
    public static func scanLine(_ r: ReposResponse, format: Format) -> String {
        guard !r.scanning else { return Copy.scanning }
        return format.ageLine(r.scannedAt, never: Copy.neverScanned, Copy.scannedAgo)
    }
}
