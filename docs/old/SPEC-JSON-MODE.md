# Spec: Migration vers le mode JSON (claude -p)

## Contexte

Actuellement, l'application utilise **tmux** pour maintenir des sessions Claude Code interactives :
- Une session tmux est créée par projet/channel
- Les messages Slack sont envoyés via `sendToTmux()` (simulation de frappe)
- La sortie est capturée via `captureTmuxOutput()` qui parse l'ASCII/TUI de Claude Code
- Problème : parsing fragile du rendu terminal (ANSI, couleurs, layout)

## Solution proposée

Utiliser le mode **headless** de Claude Code (`-p` / `--print`) avec sortie JSON structurée.

### Avantages

- Sortie JSON propre, pas de parsing ASCII/ANSI
- Métadonnées incluses (tokens, durée, session_id)
- Plus simple à maintenir
- Meilleur contrôle sur ce qu'on affiche dans Slack

## Commande Claude Code

```bash
# Nouvelle session (premier message ou après reset)
claude -p "<prompt>" \
  --dangerously-skip-permissions \
  --output-format json

# Continuer une session existante
claude -p "<prompt>" \
  --dangerously-skip-permissions \
  --output-format json \
  --resume <session_id>
```

### Format de sortie JSON

```json
{
  "result": "Le texte de la réponse de Claude...",
  "session_id": "550e8400-e29b-41d4-a716-446655440000",
  "usage": {
    "input_tokens": 1234,
    "output_tokens": 5678,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  },
  "duration_ms": 1234
}
```

## Stockage des sessions

### Structure

```go
// Map thread-safe pour stocker les session_id Claude par channel Slack
var claudeSessionIDs sync.Map  // channelID (string) -> session_id (string)
```

### Persistance (optionnel)

Ajouter au fichier de config existant (`~/.claude-code-slack-anywhere.json`) :

```json
{
  "bot_token": "xoxb-...",
  "app_token": "xapp-...",
  "user_id": "U...",
  "sessions": {
    "project-name": "C1234567890"
  },
  "claude_sessions": {
    "C1234567890": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

## Commandes Slack

### Commandes existantes à conserver

| Commande | Description | Changement |
|----------|-------------|------------|
| `!ping` | Health check | Aucun |
| `!help` | Aide | Mettre à jour le texte |
| `!list` | Liste les sessions | Afficher les session_id Claude |
| `!c <cmd>` | Exécuter commande shell | Aucun |

### Commandes à modifier

| Commande | Avant (tmux) | Après (JSON mode) |
|----------|--------------|-------------------|
| `!new <name>` | Crée session tmux | Crée channel, premier message = nouvelle session Claude |
| `!continue <name>` | Continue session tmux | Idem `!new` (la continuation est automatique via session_id) |
| `!kill <name>` | Tue session tmux | Supprime le session_id stocké |
| `!output` | Capture écran tmux | À supprimer ou remplacer |

### Nouvelle commande

| Commande | Description |
|----------|-------------|
| `!reset` | Efface le session_id du channel → prochain message = nouvelle conversation |

#### Implémentation `!reset`

```go
if strings.HasPrefix(text, "!reset") {
    claudeSessionIDs.Delete(channelID)
    sendMessage(config, channelID, ":arrows_counterclockwise: Session reset - next message starts fresh!")
    return
}
```

## Flow de traitement des messages

```
┌─────────────────────────────────────────────────────────────────┐
│                    Message Slack reçu                            │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │ Est-ce une      │
                    │ commande `!` ?  │
                    └─────────────────┘
                      │           │
                     Oui         Non
                      │           │
                      ▼           ▼
            ┌──────────────┐   ┌─────────────────────────┐
            │ Traiter la   │   │ Channel a un session_id?│
            │ commande     │   └─────────────────────────┘
            └──────────────┘         │           │
                                    Oui         Non
                                     │           │
                                     ▼           ▼
                          ┌─────────────────┐  ┌──────────────────┐
                          │ claude -p ...   │  │ claude -p ...    │
                          │ --resume <id>   │  │ (sans --resume)  │
                          └─────────────────┘  └──────────────────┘
                                     │           │
                                     └─────┬─────┘
                                           │
                                           ▼
                              ┌─────────────────────────┐
                              │ Parser JSON response    │
                              │ - Extraire result       │
                              │ - Stocker session_id    │
                              └─────────────────────────┘
                                           │
                                           ▼
                              ┌─────────────────────────┐
                              │ Envoyer result à Slack  │
                              └─────────────────────────┘
```

## Implémentation principale

### Fonction callClaude

```go
type ClaudeResponse struct {
    Result    string `json:"result"`
    SessionID string `json:"session_id"`
    Usage     struct {
        InputTokens  int `json:"input_tokens"`
        OutputTokens int `json:"output_tokens"`
    } `json:"usage"`
    DurationMs int `json:"duration_ms"`
}

func callClaude(prompt string, channelID string, workDir string) (*ClaudeResponse, error) {
    args := []string{
        "-p", prompt,
        "--dangerously-skip-permissions",
        "--output-format", "json",
    }

    // Resume si session existante pour ce channel
    if sid, ok := claudeSessionIDs.Load(channelID); ok {
        args = append(args, "--resume", sid.(string))
    }

    cmd := exec.Command("claude", args...)
    cmd.Dir = workDir

    output, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("claude error: %w", err)
    }

    var resp ClaudeResponse
    if err := json.Unmarshal(output, &resp); err != nil {
        return nil, fmt.Errorf("JSON parse error: %w", err)
    }

    // Sauvegarder le session_id pour les prochains messages
    claudeSessionIDs.Store(channelID, resp.SessionID)

    return &resp, nil
}
```

### Traitement des messages

```go
// Dans la section "message in session channel"
sessionName := cfgMgr.GetSessionByChannel(channelID)
if sessionName != "" {
    addReaction(config, channelID, event.TS, "eyes")

    workDir := filepath.Join(getProjectsDir(config), sessionName)

    // Préparer le prompt (avec contexte remote + images si présentes)
    prompt := buildPrompt(text, event.Files, config)

    // Appeler Claude en mode JSON
    resp, err := callClaude(prompt, channelID, workDir)
    if err != nil {
        addReaction(config, channelID, event.TS, "x")
        sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Error: %v", err))
        return
    }

    // Envoyer la réponse
    addReaction(config, channelID, event.TS, "white_check_mark")
    removeReaction(config, channelID, event.TS, "eyes")

    // Formater et envoyer (gérer les messages longs)
    sendFormattedResponse(config, channelID, event.TS, resp)
}
```

## Gestion des réponses longues

Le champ `result` peut être très long. Stratégies :

1. **Découpage** : Slack limite à ~4000 caractères par message
2. **Thread** : Envoyer les morceaux en thread
3. **File upload** : Si trop long, uploader comme fichier texte

```go
func sendFormattedResponse(config *Config, channelID, threadTS string, resp *ClaudeResponse) {
    result := resp.Result

    // Footer avec stats
    footer := fmt.Sprintf("\n\n_tokens: %d in / %d out • %dms_",
        resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.DurationMs)

    if len(result) < 3500 {
        sendMessageToThread(config, channelID, threadTS, result + footer)
        return
    }

    // Découper en chunks
    chunks := splitMessage(result, 3500)
    for i, chunk := range chunks {
        msg := chunk
        if i == len(chunks)-1 {
            msg += footer
        }
        sendMessageToThread(config, channelID, threadTS, msg)
    }
}
```

## Streaming (optionnel, phase 2)

Pour des réponses en temps réel, utiliser `--output-format stream-json` :

```bash
claude -p "prompt" \
  --dangerously-skip-permissions \
  --output-format stream-json \
  --resume <session_id>
```

Chaque ligne est un événement JSON. Parser et envoyer progressivement à Slack.

## Migration

### Ce qui disparaît

- `createTmuxSession()`
- `killTmuxSession()`
- `tmuxSessionExists()`
- `sendToTmux()`
- `captureTmuxOutput()`
- `streamOutputToThread()` (remplacé par parsing JSON)
- `listTmuxSessions()`

### Ce qui reste

- Gestion des channels Slack
- Système de sessions (channel ↔ projet)
- Commandes `!`
- Téléchargement d'images
- Formatage des messages Slack

## Questions ouvertes

1. **Persistance des session_id** : En mémoire seulement ou dans le fichier config ?
   - En mémoire : perdu au restart → nouvelle session automatique
   - Config : conservé → peut reprendre après restart

2. **Timeout** : Ajouter un timeout sur `exec.Command` pour éviter les blocages ?

3. **Streaming** : Implémenter le streaming JSON pour feedback en temps réel ?

4. **!continue vs !new** : Fusionner en une seule commande puisque la continuation est automatique ?
