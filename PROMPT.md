# snapwatch — prompt de construction

Construis `snapwatch`, un outil CLI Go qui surveille un répertoire, snapshotte
chaque modification et affiche en live dans le navigateur l'historique des
diffs. Cas d'usage : regarder ce qu'un agent LLM fait sur un projet, même
non versionné.

Usage cible :

```
snapwatch [--addr 127.0.0.1:7777] [--debounce 300ms] <dir>
```

Ouvre `http://127.0.0.1:7777`, et c'est tout.

## Principes

- Un seul binaire, zéro build front, zéro config. Ce qui n'est pas dans ce
  document n'existe pas : pas d'auth, pas de multi-projets, pas de thèmes,
  pas de restauration d'état, pas de plugins.
- Le plus court chemin qui marche. Réutilise git pour tout ce qui est
  stockage/diff/renommage ; n'écris pas de moteur de diff.
- Perf : la page doit rester fluide avec 5 000 snapshots et des diffs de
  plusieurs milliers de lignes. Rien de bloquant sur le chemin fsnotify →
  commit ; le rendu HTML est fait à la demande et mis en cache par SHA.
- Code idiomatique Go, packages plats, pas d'interface avec une seule
  implémentation, pas de couche d'abstraction « pour plus tard ».

## Architecture

### Stockage : shadow git

- Repo git dédié dans `$XDG_DATA_HOME/snapwatch/<sha256(abs dir)[:16]>/`
  (`git init` si absent), utilisé via `git --git-dir=<repo> --work-tree=<dir>`.
  Le `.git` éventuel du projet n'est jamais touché.
- Un `info/exclude` du shadow repo contient au minimum
  `.git/`, `.jj/`, `node_modules/`, `target/`, `result`, `.direnv/`, `*.log`.
  Le `.gitignore` du projet est respecté naturellement par git.
- Snapshot = `git add -A && git commit -q --allow-empty-message -m ""`
  (avec `user.name=snapwatch`, `user.email=snapwatch@local`, `commit.gpgsign=false`
  passés en `-c`). Ne pas commiter si `git diff --cached --quiet` ne renvoie rien.
- Au démarrage : snapshot initial (état de départ), puis lancement du watcher.
- Appelle git via `os/exec`. Pas de go-git.

### Watcher

- `github.com/fsnotify/fsnotify`, récursif (ajoute les nouveaux sous-dossiers
  aux événements Create, ignore les répertoires de la liste d'exclusion).
- Debounce global : un timer réarmé à chaque événement ; à expiration, un
  snapshot. Une seule goroutine de snapshot, jamais deux commits concurrents.
- Après chaque commit réussi, diffuser l'événement `snapshot` sur SSE.

### Serveur HTTP

`net/http` stdlib, `html/template` embarqué via `embed`. Routes :

| Route | Rôle |
|---|---|
| `GET /` | Page complète : timeline + panneau diff vide |
| `GET /snapshots?after=<sha>` | Fragment htmx : entrées de timeline plus récentes que `<sha>` (toutes si absent) |
| `GET /diff/<sha>` | Fragment htmx : diff de ce snapshot vs son parent |
| `GET /diff/<from>..<to>` | Fragment htmx : diff cumulé entre deux snapshots |
| `GET /events` | SSE, événement `snapshot` avec le SHA en data |

Données : `git log --format=%H%x00%ct%x00 --shortstat` pour la timeline
(SHA, timestamp, +/- lignes, nb fichiers), `git diff -M <a> <b>` pour le diff.

### Rendu du diff (côté serveur, Go)

- Parse le unified diff avec `github.com/sourcegraph/go-diff/diff`.
- Rendu HTML side-by-side dans un template Go : pour chaque fichier, en-tête
  (chemin, renommage, ajouté/supprimé, +N −M, repliable), puis table deux
  colonnes ; les lignes de contexte hors hunks ne sont pas affichées.
- Coloration syntaxique avec `github.com/alecthomas/chroma/v2`, lexer choisi
  par extension, appliquée ligne par ligne (`ponytail:` ceiling connu —
  les tokens multilignes sont mal colorés ; passer à un highlight du fichier
  complet si ça gêne).
- Cache mémoire `map[string]template.HTML` clé = `from..to`, protégé par un
  `sync.Mutex`, borné à 256 entrées (éviction arbitraire, ça suffit).

### Front

- Une page `index.html` embarquée. htmx + son extension SSE, servis depuis
  `embed` (vendorés dans `static/`, pas de CDN). CSS maison, < 150 lignes,
  pas de framework.
- Layout : timeline à gauche (colonne fixe, scrollable), diff à droite.
- Timeline : une ligne par snapshot — heure `HH:MM:SS`, `+N −M`, nb fichiers.
  Nouveau snapshot arrivé par SSE → `hx-get="/snapshots?after=<dernier>"`
  déclenché par `sse:snapshot`, inséré en haut (`hx-swap="afterbegin"`).
  Si l'utilisateur est en haut de la timeline, le diff du nouveau snapshot
  se charge automatiquement (« suivre »), sinon on ne bouge pas.
- Clic sur un snapshot → charge `/diff/<sha>` dans le panneau droit.
  Shift-clic sur un second → `/diff/<a>..<b>`. Quelques lignes de JS vanilla
  pour la sélection, le reste est htmx.
- Le diff : fichiers repliables, side-by-side, lignes ajoutées/supprimées
  colorées, numéros de ligne des deux côtés, police monospace, thème sombre.

## Fichiers attendus

```
go.mod
main.go          flags, démarrage watcher + serveur
shadow.go        repo shadow : init, snapshot, log, diff
watch.go         fsnotify récursif + debounce
render.go        parse diff → HTML side-by-side + chroma + cache
server.go        routes, SSE, templates
templates/*.html
static/htmx.min.js static/sse.js static/style.css
flake.nix        devShell (go, gopls) + package via buildGoModule
README.md        10 lignes : quoi, install, usage
```

## Vérification

- `go vet ./...` et `go build` propres.
- Un test `shadow_test.go` : dans un `t.TempDir()`, écrire un fichier,
  snapshot, le modifier, snapshot, vérifier que `log` renvoie 3 entrées
  (initial + 2) et que le diff du dernier contient la ligne modifiée.
- Un test `render_test.go` : un petit unified diff avec un renommage et un
  hunk → le HTML contient les deux chemins et les lignes `+`/`-` dans les
  bonnes colonnes.
- Démo manuelle décrite dans le README : `snapwatch /tmp/demo` dans un
  terminal, `echo x >> /tmp/demo/a.txt` dans un autre, la timeline se met
  à jour sans recharger la page.

## Anti-objectifs (ne pas faire)

Restauration d'un snapshot, purge/rotation de l'historique, multi-répertoires,
config file, détection « agent vs humain », authentification, mode daemon,
tests de l'UI.
