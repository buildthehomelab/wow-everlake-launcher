-- Every primary profession on the client's Professions page.
--
-- In 1.60 that page is ProfessionsFrame.BookPage: its ProfessionsContentFrame has two sections for the primary
-- professions in the client's own slots (PrimaryProfession1/2; one used on Everlake, which keeps the second slot free
-- so every profession can be learned) above Cooking, Fishing and First Aid. A scrolling list over the two sections
-- shows every known primary profession instead, in sections of their size: spells, rank, rank bar, unlearn X. The
-- secondary professions (Cooking, Fishing, First Aid) follow as sections of the same kind, so one list covers the whole
-- page. In combat the page is left as the client made it.
local _, addon = ...
local P = addon.Professions
if not P then return end

local GATHER_MAIN = { [186] = 2580, [182] = 2383 }                -- Mining: Find Minerals, Herbalism: Find Herbs
local EXTRA_SPELLS = { [333] = { 13262 }, [186] = { 2656 }, [755] = { 31252 }, [773] = { 51005 } }   -- Disenchant, Smelting, Prospecting, Milling
local BAR_COLORS = {
    [171] = { 0.25, 0.75, 0.3 }, [164] = { 0.85, 0.35, 0.1 }, [333] = { 0.6, 0.35, 0.9 }, [202] = { 0.9, 0.7, 0.2 },
    [773] = { 0.3, 0.55, 0.9 }, [755] = { 0.2, 0.75, 0.8 }, [165] = { 0.65, 0.45, 0.2 }, [197] = { 0.8, 0.3, 0.6 },
    [186] = { 0.6, 0.6, 0.7 }, [182] = { 0.35, 0.7, 0.2 }, [393] = { 0.7, 0.5, 0.3 },
    [185] = { 0.85, 0.8, 0.65 }, [356] = { 0.3, 0.45, 0.85 }, [129] = { 0.45, 0.85, 0.1 },
}

-- English names in the client's per-profession atlases (Profession-overview-Card-Blacksmithing, ...)
local ATLAS_NAME = {
    [171] = "Alchemy", [164] = "Blacksmithing", [333] = "Enchanting", [202] = "Engineering", [773] = "Inscription",
    [755] = "Jewelcrafting", [165] = "Leatherworking", [197] = "Tailoring", [186] = "Mining", [182] = "Herbalism", [393] = "Skinning",
    [185] = "Cooking", [356] = "Fishing", [129] = "FirstAid",
}

local secondaryArt   -- the client's secondary card atlases by profession name (read with the page)

-- SetAtlas on 1.60 does not say when an atlas is missing (a missing card drew nothing at all): read it back instead
local function TrySetAtlas(texture, atlas)
    if not (texture and atlas) then return false end
    texture:SetAtlas(atlas)
    return texture:GetAtlas() == atlas
end

-- the rank a spell shows in the client (its subtext: Apprentice, Journeyman, ...)
local function SpellRank(id)
    local text
    if C_Spell and C_Spell.GetSpellSubtext then text = C_Spell.GetSpellSubtext(id)
    elseif GetSpellSubtext then text = GetSpellSubtext(id) end
    if text and text ~= "" then return text end
end

local function RankTitle(maxRank)
    if not maxRank or maxRank <= 0 then return "" end
    if maxRank <= 75 then return "Apprentice" elseif maxRank <= 150 then return "Journeyman"
    elseif maxRank <= 225 then return "Expert" elseif maxRank <= 300 then return "Artisan" end
    return "Master"
end

local function IsPassive(id)
    if C_Spell and C_Spell.IsSpellPassive then return C_Spell.IsSpellPassive(id) end
    return IsPassiveSpell and IsPassiveSpell(id)
end

-- The spells the client's own page shows for a profession it knows (GetProfessions index): its spellbook entries.
local function BookSpells(index)
    local _, _, _, _, numSpells, offset = GetProfessionInfo(index)
    local ids = {}
    for i = 1, numSpells or 0 do
        local slot, id = (offset or 0) + i, nil
        if C_SpellBook and C_SpellBook.GetSpellBookItemInfo and Enum and Enum.SpellBookSpellBank then
            local info = C_SpellBook.GetSpellBookItemInfo(slot, Enum.SpellBookSpellBank.Player)
            id = info and (info.spellID or info.actionID)
            if info and info.isPassive then id = nil end
        elseif GetSpellBookItemInfo then
            local _, spellId = GetSpellBookItemInfo(slot, BOOKTYPE_PROFESSION or "spell")
            id = spellId
        end
        if id and not IsPassive(id) then ids[#ids + 1] = id end
    end
    return ids
end

-- The client's professions: primaries by skill line, and its secondary ones in its order.
local function ClientProfessions()
    local primary, secondary = {}, {}
    if not GetProfessions or not GetProfessionInfo then return primary, secondary end
    local t = { GetProfessions() }
    for k = 1, 6 do
        local index = t[k]
        if index then
            local name, icon, rank, maxRank, _, _, skillLine = GetProfessionInfo(index)
            local entry = { index = index, name = name, icon = icon, rank = rank, maxRank = maxRank, skill = skillLine }
            if k <= 2 then
                if skillLine then primary[skillLine] = entry end
            elseif name then
                secondary[#secondary + 1] = entry
            end
        end
    end
    return primary, secondary
end

-- Every profession in the character's skill list, by skill line: GetProfessionInfo takes any skill list index, not
-- only the ones GetProfessions hands out, so it gives rank, max rank and spellbook spells for every profession.
local function SkillListProfessions()
    local bySkill = {}
    if not GetProfessionInfo then return bySkill end
    for index = 1, 128 do
        local ok, name, icon, rank, maxRank, numSpells, offset, skillLine = pcall(GetProfessionInfo, index)
        if ok and name and skillLine and not bySkill[skillLine] then
            bySkill[skillLine] = { index = index, name = name, icon = icon, rank = rank, maxRank = maxRank,
                numSpells = numSpells, offset = offset, skill = skillLine }
        end
    end
    return bySkill
end

-- spells of a primary profession the client has no slot for: its own spell (or the gathering tracker), then extras
local function StaticSpells(entry)
    local spells = {}
    local main = GATHER_MAIN[entry.skill] or entry.castId
    if main and P.Knows(main) then spells[#spells + 1] = main end
    for _, id in ipairs(EXTRA_SPELLS[entry.skill] or {}) do
        if P.Knows(id) then spells[#spells + 1] = id end
    end
    return spells
end

-- Unlearn, as the client's own red X: a confirmation, then AbandonSkill(skill line). The server unlearns any primary
-- profession by skill line (CMSG_UNLEARN_SKILL), in the client's slots or not. Only a new entry in the client's dialog
-- table: assigning the global itself would taint every popup.
if StaticPopupDialogs then
    StaticPopupDialogs.EVERLAKE_UNLEARN_PROFESSION = {
        text = UNLEARN_SKILL or "Do you really want to unlearn %s?",
        button1 = UNLEARN or ACCEPT or "Unlearn",
        button2 = CANCEL or "Cancel",
        OnAccept = function(_, skill) if skill and AbandonSkill then AbandonSkill(skill) end end,
        timeout = 0,
        whileDead = 1,
        hideOnEscape = 1,
        showAlert = 1,
    }
end

local function TryCreate(frameType, name, parent, ...)
    for _, template in ipairs({ ... }) do
        local ok, f = pcall(CreateFrame, frameType, name, parent, template)
        if ok and f then return f, template end
    end
    return CreateFrame(frameType, name, parent)
end

local function Panel(parent)
    local f, template = TryCreate("Frame", nil, parent, "InsetFrameTemplate")
    if not template then
        local bg = f:CreateTexture(nil, "BACKGROUND")
        bg:SetAllPoints()
        bg:SetColorTexture(0, 0, 0, 0.35)
    end
    return f
end

local function SpellButton(parent)
    local b = CreateFrame("Button", nil, parent, "SecureActionButtonTemplate")
    b:SetSize(40, 40)
    b:RegisterForClicks("AnyUp", "AnyDown")
    b.back = b:CreateTexture(nil, "BACKGROUND")
    b.back:SetPoint("TOPLEFT", -2, 2)
    b.back:SetPoint("BOTTOMRIGHT", 2, -2)
    b.back:SetColorTexture(0, 0, 0, 0.9)
    b.icon = b:CreateTexture(nil, "BORDER")
    b.icon:SetAllPoints()
    -- the client's profession spell button: a shadow round the icon and the profession slot frame over it
    b.shadow = b:CreateTexture(nil, "ARTWORK")
    b.shadow:SetSize(48, 48)
    b.shadow:SetPoint("TOPLEFT", -4, 4)
    b.slot = b:CreateTexture(nil, "OVERLAY")
    b.slot:SetSize(40, 40)
    b.slot:SetPoint("TOPLEFT")
    if TrySetAtlas(b.slot, "Professions-Slot-Frame") then
        b.back:Hide()
        if not TrySetAtlas(b.shadow, "UI-HUD-ActionBar-IconFrame-FlyoutBorderShadow") then b.shadow:Hide() end
    else
        b.slot:Hide()
        b.shadow:Hide()
        b.icon:SetTexCoord(0.07, 0.93, 0.07, 0.93)
    end
    b:SetHighlightTexture("Interface\\Buttons\\ButtonHilight-Square", "ADD")
    b.label = b:CreateFontString(nil, "OVERLAY", "GameFontNormal")
    b.label:SetPoint("TOPLEFT", b, "TOPRIGHT", 8, -4)
    b.sub = b:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    b.sub:SetPoint("TOPLEFT", b.label, "BOTTOMLEFT", 0, -2)
    b:SetScript("OnEnter", function(self)
        if self.spellId then
            GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
            if GameTooltip.SetSpellByID then GameTooltip:SetSpellByID(self.spellId) else GameTooltip:SetText(self.label:GetText() or "") end
            GameTooltip:Show()
        end
    end)
    b:SetScript("OnLeave", function() GameTooltip:Hide() end)
    return b
end

local function SetSpell(b, id, sub)
    local name, icon = P.SpellNameIcon(id)
    b.spellId = id
    b.icon:SetTexture(icon)
    b.label:SetText(name or "")
    b.sub:SetText(sub or "")
    b:SetAttribute("type", "spell")
    b:SetAttribute("spell", name)
    b:Show()
end

local function Bar(parent)
    local bar = CreateFrame("StatusBar", nil, parent)
    bar:SetSize(440, 17)   -- the client's profession bar
    bar:SetStatusBarTexture("Interface\\TargetingFrame\\UI-StatusBar")
    bar.bg = bar:CreateTexture(nil, "BACKGROUND")
    bar.bg:SetPoint("TOPLEFT")
    bar.bg:SetSize(440, 22)
    if not TrySetAtlas(bar.bg, "Profession-ProgressBar-BG") then
        bar.bg:ClearAllPoints()
        bar.bg:SetPoint("TOPLEFT", -1, 1)
        bar.bg:SetPoint("BOTTOMRIGHT", 1, -1)
        bar.bg:SetColorTexture(0, 0, 0, 0.9)
    end
    bar.text = bar:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    bar.text:SetPoint("CENTER")
    return bar
end

local function SetBar(bar, skill, name, rank, maxRank, fillAtlas)
    if rank and maxRank and maxRank > 0 then
        local fill = bar:GetStatusBarTexture()
        if fillAtlas and fill and TrySetAtlas(fill, fillAtlas) then
            bar:SetStatusBarColor(1, 1, 1)
        else
            bar:SetStatusBarTexture("Interface\\TargetingFrame\\UI-StatusBar")
            local c = BAR_COLORS[skill] or { 0.9, 0.6, 0.1 }
            bar:SetStatusBarColor(c[1], c[2], c[3])
        end
        bar:SetMinMaxValues(0, maxRank)
        bar:SetValue(rank)
        bar.text:SetText(string.format("%s %d/%d", name or "", rank, maxRank))
        bar:Show()
    else
        bar:Hide()
    end
end

local function UnlearnButton(parent)
    local b = CreateFrame("Button", nil, parent)
    b:SetSize(24, 24)
    b:SetNormalTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Up")
    b:SetPushedTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Down")
    b:SetHighlightTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Highlight", "ADD")
    b:SetScript("OnClick", function(self)
        if self.skill then StaticPopup_Show("EVERLAKE_UNLEARN_PROFESSION", self.profession, nil, self.skill) end
    end)
    b:SetScript("OnEnter", function(self)
        GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
        GameTooltip:SetText(UNLEARN or "Unlearn")
        GameTooltip:Show()
    end)
    b:SetScript("OnLeave", function() GameTooltip:Hide() end)
    return b
end

-- Art the client's own (hidden) first section carries, read at run time: its profession card names the profession it
-- shows (Profession-overview-Card-<name>); its bar fill and red X atlases are reused, the fill with the name swapped.
local art
local function ClientArt(p1)
    if art then return art end
    local found = { cross = {}, atlases = {} }
    local function walk(frame, depth)
        if depth > 4 then return end
        if frame.GetStatusBarTexture then
            local t = frame:GetStatusBarTexture()
            local a = t and t.GetAtlas and t:GetAtlas()
            if a then found.barAtlas = found.barAtlas or a end
        end
        for _, r in ipairs({ frame:GetRegions() }) do
            local a = r.GetObjectType and r:GetObjectType() == "Texture" and r:GetAtlas()
            if a then
                found.atlases[#found.atlases + 1] = a
                local card = a:match("^Profession%-overview%-Card%-(.+)$")
                if card then found.cardName = card end
                if a:find("crossmark", 1, true) then found.cross[#found.cross + 1] = a end
            end
        end
        for _, c in ipairs({ frame:GetChildren() }) do walk(c, depth + 1) end
    end
    walk(p1, 0)
    if not found.cardName then return nil end   -- the client has not filled its section yet: try again later
    -- the bar fill: the status bar texture when it names the profession, else any other atlas naming it, one with
    -- "Bar" or "Fill" in its name first
    local name = found.cardName
    if found.barAtlas and found.barAtlas:find(name, 1, true) then found.fill = found.barAtlas end
    if not found.fill then
        for _, a in ipairs(found.atlases) do
            if a:find(name, 1, true) and not a:find("overview%-Card") then
                if a:find("Bar") or a:find("Fill") then found.fill = a break end
                found.fill = found.fill or a
            end
        end
    end
    for _, a in ipairs(found.cross) do
        if a:find("%-pressed$") then found.crossPushed = a
        elseif a:find("%-highlight$") or a:find("%-hover$") then found.crossHighlight = a
        elseif not a:find("%-disabled$") then found.crossNormal = found.crossNormal or a end
    end
    if not found.crossNormal and found.crossPushed then found.crossNormal = found.crossPushed:gsub("%-pressed$", "") end
    art = found
    return art
end

local function FillAtlas(skill)
    local name = ATLAS_NAME[skill]
    if not (art and art.fill and art.cardName and name) then return nil end
    local swapped, n = art.fill:gsub(art.cardName, name)
    return n > 0 and swapped or nil
end

-- The client's profession bar (1.60, from /everlake prof bar): a 440x17 frame of four ARTWORK textures:
-- Profession-ProgressBar-BG (440x22, top left), Skillbar_Fill_Flipbook_<profession> (440x17 at 2,-3, its width the rank),
-- Skillbar_Flare_<profession> (53x15 on the fill's right end) and Profession-ProgressBar-frame (440x22, top left).
-- The fill is a flipbook: the client plays it with a FlipBook animation, copied here from its own hidden bar (rows,
-- columns, frames, duration) so ours plays the same; without one the sheet would show whole, as stripes.
local BAR_W, BAR_H, BAR_FRAME_H, FILL_W = 440, 17, 22, 436

local function BarModel(p1)
    if type(p1.StatusBar) == "table" and p1.StatusBar.GetRegions then return p1.StatusBar end
end

local flipbooks   -- { fill = {...}, flare = {...} } read once from the client's bar
local function ReadFlipbooks(model)
    if flipbooks then return flipbooks end
    local found = {}
    local function scan(frame)
        if not frame.GetAnimationGroups then return end
        for _, group in ipairs({ frame:GetAnimationGroups() }) do
            for _, anim in ipairs({ group:GetAnimations() }) do
                if anim:GetObjectType() == "FlipBook" then
                    local target = anim:GetTarget()
                    local atlas = target and target.GetAtlas and target:GetAtlas() or ""
                    local which = atlas:find("Flare") and "flare" or (atlas:find("Fill") and "fill") or nil
                    if which and not found[which] then
                        found[which] = {
                            rows = anim:GetFlipBookRows(), columns = anim:GetFlipBookColumns(), frames = anim:GetFlipBookFrames(),
                            width = anim:GetFlipBookFrameWidth(), height = anim:GetFlipBookFrameHeight(),
                            duration = anim:GetDuration(), looping = group:GetLooping(),
                        }
                    end
                end
            end
        end
    end
    scan(model)
    for _, child in ipairs({ model:GetChildren() }) do scan(child) end
    if model:GetParent() then scan(model:GetParent()) end
    flipbooks = found
    return found
end

local function PlayFlipbook(owner, texture, fb)
    local group = owner:CreateAnimationGroup()
    local anim = group:CreateAnimation("FlipBook")
    anim:SetTarget(texture)
    anim:SetFlipBookRows(fb.rows or 1)
    anim:SetFlipBookColumns(fb.columns or 1)
    anim:SetFlipBookFrames(fb.frames or 1)
    anim:SetFlipBookFrameWidth(fb.width or 0)
    anim:SetFlipBookFrameHeight(fb.height or 0)
    anim:SetDuration(fb.duration or 1)
    group:SetLooping((fb.looping and fb.looping ~= "NONE") and fb.looping or "REPEAT")
    group:Play()
    return group
end

local function ProfessionBar(parent, model)
    local bar = CreateFrame("Frame", nil, parent)
    bar:SetSize(BAR_W, BAR_H)
    bar.bg = bar:CreateTexture(nil, "ARTWORK", nil, 0)
    bar.bg:SetSize(BAR_W, BAR_FRAME_H)
    bar.bg:SetPoint("TOPLEFT")
    if not TrySetAtlas(bar.bg, "Profession-ProgressBar-BG") then bar:Hide() return nil end
    bar.fill = bar:CreateTexture(nil, "ARTWORK", nil, 1)
    bar.fill:SetSize(FILL_W, BAR_H)
    bar.fill:SetPoint("TOPLEFT", 2, -3)
    bar.flare = bar:CreateTexture(nil, "ARTWORK", nil, 2)
    bar.flare:SetSize(53, 15)
    bar.flare:SetPoint("RIGHT", bar.fill, "RIGHT")
    bar.frame = bar:CreateTexture(nil, "ARTWORK", nil, 3)
    bar.frame:SetSize(BAR_W, BAR_FRAME_H)
    bar.frame:SetPoint("TOPLEFT")
    bar.frame:SetAtlas("Profession-ProgressBar-frame")
    local textHolder = CreateFrame("Frame", nil, bar)
    textHolder:SetAllPoints()
    textHolder:SetFrameLevel(bar:GetFrameLevel() + 5)
    bar.text = textHolder:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    bar.text:SetPoint("CENTER", 0, -2)
    local fb = ReadFlipbooks(model)
    bar.flipFill = fb.fill
    if fb.fill then PlayFlipbook(bar, bar.fill, fb.fill) end
    if fb.flare then PlayFlipbook(bar, bar.flare, fb.flare) end
    bar.isProfessionBar = true
    return bar
end

-- one profession on the bar: its own fill and flare (the name swapped in the client's atlas names), width = rank
local function SetProfessionBar(bar, skill, name, rank, maxRank)
    local mine = ATLAS_NAME[skill]
    local client = art and art.cardName
    local function own(atlas)
        if not (atlas and client and mine) then return atlas end
        return (atlas:gsub(client, mine))
    end
    local fillAtlas = art and art.fill and own(art.fill) or nil
    local flareAtlas = fillAtlas and fillAtlas:gsub("Fill_Flipbook", "Flare") or nil
    if not (rank and maxRank and maxRank > 0) then bar:Hide() return end
    -- a flipbook fill without its animation would show the whole sheet: then a plain coloured fill
    local animated = bar.flipFill and fillAtlas and TrySetAtlas(bar.fill, fillAtlas)
    if not animated then
        bar.fill:SetTexture("Interface\\TargetingFrame\\UI-StatusBar")
        local c = BAR_COLORS[skill] or { 0.9, 0.6, 0.1 }
        bar.fill:SetVertexColor(c[1], c[2], c[3])
    else
        bar.fill:SetVertexColor(1, 1, 1)
    end
    local hasFlare = flareAtlas and TrySetAtlas(bar.flare, flareAtlas)
    local fraction = math.max(0, math.min(1, rank / maxRank))
    local width = math.max(1, FILL_W * fraction)
    bar.fill:SetWidth(width)
    bar.fill:SetShown(fraction > 0)
    -- the flare sits on the fill's right end; on a fill narrower than itself it would stick out left of the bar
    bar.flare:SetShown(hasFlare and width >= 53 and fraction < 1 and true or false)
    bar.text:SetText(string.format("%s %d/%d", name or "", rank, maxRank))
    bar:Show()
end

-- the card atlases of the client's secondary sections, by the profession name each one shows
local function ReadSecondaryArt(frames)
    local found = {}
    for _, frame in ipairs(frames) do
        local name, atlas, w, h
        local function walk(f2, depth)
            if depth > 3 then return end
            for _, r in ipairs({ f2:GetRegions() }) do
                local kind = r.GetObjectType and r:GetObjectType()
                if kind == "Texture" and not atlas and r:GetDrawLayer() == "BACKGROUND" and r:GetAtlas() then
                    atlas = r:GetAtlas()
                    w, h = r:GetSize()
                elseif kind == "FontString" and not name and r:GetText() and r:GetText() ~= "" then
                    name = r:GetText()
                end
            end
            for _, c in ipairs({ f2:GetChildren() }) do walk(c, depth + 1) end
        end
        walk(frame, 0)
        if name and atlas and w and h and w > 0 and h > 0 then found[name] = { atlas = atlas, w = w, h = h } end
    end
    return found
end

local list, content, page
local sections = {}
local SECTION_GAP, SPELL_STEP = 6, 46
local sectionW, sectionH = 663, 142

local function PrimarySection(i)
    local f = sections[i]
    if not f then
        f = CreateFrame("Frame", nil, content)
        f.card = f:CreateTexture(nil, "BACKGROUND")
        f.card:SetAllPoints()
        f.plain = Panel(f)   -- when the client has no card for this profession
        f.plain:SetAllPoints()
        f.plain:SetFrameLevel(f:GetFrameLevel())
        f.header = f:CreateFontString(nil, "OVERLAY", "GameFontNormal")
        f.header:SetPoint("TOPLEFT", 18, -26)
        f.spells = {}
        f.bar = Bar(f)
        f.unlearn = UnlearnButton(f)
        f.unlearn:SetPoint("LEFT", f.bar, "RIGHT", 6, 0)
        sections[i] = f
    end
    f:SetWidth(sectionW)
    if f.bar.isProfessionBar ~= true then   -- the profession bar keeps the client's own place in the section
        f.bar:ClearAllPoints()
        f.bar:SetPoint("TOPLEFT", f, "TOPLEFT", math.min(math.floor(sectionW * 0.32), sectionW - 440 - 44), -66)
    end
    return f
end

local function FillPrimary(f, entry, spells, p1)
    f.header:SetText(entry.name)
    -- the profession's own spell first (the spellbook lists Bait and Tackle before Fishing)
    table.sort(spells, function(a, b)
        local an, bn = P.SpellNameIcon(a), P.SpellNameIcon(b)
        local am, bm = an == entry.name, bn == entry.name
        if am ~= bm then return am end
        return false
    end)
    for k = 1, math.max(#spells, #f.spells) do
        local b = f.spells[k]
        if spells[k] then
            if not b then
                b = SpellButton(f)
                b:SetPoint("TOPLEFT", 14, -54 - (k - 1) * SPELL_STEP)
                f.spells[k] = b
            end
            SetSpell(b, spells[k], SpellRank(spells[k]) or (k == 1 and RankTitle(entry.maxRank) or nil))
        elseif b then
            b:Hide()
        end
    end
    if not spells[1] then   -- nothing to cast (Skinning without Tanning): the profession's icon and name
        local b = f.spells[1]
        if not b then
            b = SpellButton(f)
            b:SetPoint("TOPLEFT", 14, -54)
            f.spells[1] = b
        end
        b.spellId = nil
        b:SetAttribute("type", nil)
        b.icon:SetTexture(entry.icon)
        b.label:SetText(entry.name)
        b.sub:SetText(RankTitle(entry.maxRank))
        b:Show()
    end
    local card = ATLAS_NAME[entry.skill] and ("Profession-overview-Card-" .. ATLAS_NAME[entry.skill])
    f.card:SetTexCoord(0, 1, 0, 1)
    local hasCard = card and TrySetAtlas(f.card, card)
    -- Cooking, Fishing, First Aid: the client has only tall cards for them (224x275, with their own border). A plain
    -- panel, and on its right the card's picture through a clipping frame: the card a little larger than the clip so
    -- its border falls outside, its lower part (where the picture is) showing, its left edge faded into the panel.
    -- (Texture coordinates cannot cut an atlas here: they covered the whole sheet.)
    local secondary = not hasCard and entry.secondary and secondaryArt and secondaryArt[entry.name]
    if secondary then
        -- the primary cards' warm dark brown and thin gold edge, over the plain inset
        if type(f.warm) ~= "table" then
            f.warm = f:CreateTexture(nil, "BACKGROUND", nil, 1)
            f.warm:SetPoint("TOPLEFT", 3, -3)
            f.warm:SetPoint("BOTTOMRIGHT", -3, 3)
            f.warm:SetColorTexture(0.11, 0.08, 0.05, 1)
            f.edge = {}
            for i, side in ipairs({ { "TOPLEFT", "TOPRIGHT" }, { "BOTTOMLEFT", "BOTTOMRIGHT" } }) do
                local line = f:CreateTexture(nil, "BORDER")
                line:SetPoint(side[1], i == 1 and 3 or 3, i == 1 and -3 or 3)
                line:SetPoint(side[2], -3, i == 1 and -3 or 3)
                line:SetHeight(1)
                line:SetColorTexture(0.55, 0.42, 0.2, 0.8)
                f.edge[#f.edge + 1] = line
            end
            for i, side in ipairs({ { "TOPLEFT", "BOTTOMLEFT" }, { "TOPRIGHT", "BOTTOMRIGHT" } }) do
                local line = f:CreateTexture(nil, "BORDER")
                line:SetPoint(side[1], i == 1 and 3 or -3, -3)
                line:SetPoint(side[2], i == 1 and 3 or -3, 3)
                line:SetWidth(1)
                line:SetColorTexture(0.55, 0.42, 0.2, 0.8)
                f.edge[#f.edge + 1] = line
            end
        end
        f.warm:Show()
        for _, line in ipairs(f.edge) do line:Show() end
        if type(f.artClip) ~= "table" then
            f.artClip = CreateFrame("Frame", nil, f)
            f.artClip:SetPoint("TOPRIGHT", -4, -4)
            f.artClip:SetPoint("BOTTOMRIGHT", -4, 4)
            f.artClip:SetWidth(250)
            if f.artClip.SetClipsChildren then f.artClip:SetClipsChildren(true) end
            f.artClip:SetFrameLevel(f:GetFrameLevel() + 1)
            f.secArt = f.artClip:CreateTexture(nil, "BACKGROUND")
            f.artFade = f.artClip:CreateTexture(nil, "ARTWORK")
            f.artFade:SetPoint("TOPLEFT")
            f.artFade:SetPoint("BOTTOMLEFT")
            f.artFade:SetWidth(60)
            f.artFade:SetColorTexture(1, 1, 1, 1)
            pcall(function()
                f.artFade:SetGradient("HORIZONTAL", CreateColor(0.11, 0.08, 0.05, 1), CreateColor(0.11, 0.08, 0.05, 0))
            end)
        end
        if TrySetAtlas(f.secArt, secondary.atlas) then
            local scale = 1.2
            f.secArt:SetSize(secondary.w * scale, secondary.h * scale)
            f.secArt:ClearAllPoints()
            f.secArt:SetPoint("BOTTOMRIGHT", f.artClip, "BOTTOMRIGHT", 12, -14)
            f.secArt:SetAlpha(0.85)
            f.artClip:Show()
        else
            f.artClip:Hide()
        end
    elseif type(f.artClip) == "table" then
        f.artClip:Hide()
    end
    if not secondary and type(f.warm) == "table" then
        f.warm:Hide()
        for _, line in ipairs(f.edge) do line:Hide() end
    end
    f.card:SetShown(hasCard and true or false)
    f.plain:SetShown(not hasCard)
    local model = art and p1 and BarModel(p1)
    if model and not f.profBarTried then
        f.profBarTried = true
        local ok, bar = pcall(ProfessionBar, f, model)
        if ok and bar then
            -- the client's place for its bar in a section
            local point, relative, relativePoint, x, y = model:GetPoint(1)
            bar:ClearAllPoints()
            if point then bar:SetPoint(point, relative == p1 and f or f, relativePoint, x, y) else bar:SetPoint("TOPLEFT", f, "TOPLEFT", 200, -66) end
            f.plainBar = f.bar
            f.plainBar:Hide()
            f.bar = bar
            f.unlearn:ClearAllPoints()
            f.unlearn:SetPoint("LEFT", bar, "RIGHT", 6, -2)
        end
    end
    if f.bar.isProfessionBar == true then
        SetProfessionBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank)
    else
        SetBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank, FillAtlas(entry.skill))
    end
    if art and art.crossNormal and not f.unlearn.clientArt then   -- the client's red X
        f.unlearn.clientArt = true
        f.unlearn:SetSize(21, 21)
        f.unlearn:SetNormalAtlas(art.crossNormal)
        if art.crossPushed then f.unlearn:SetPushedAtlas(art.crossPushed) end
        if art.crossHighlight then f.unlearn:SetHighlightAtlas(art.crossHighlight, "ADD") end
    end
    f.unlearn.skill, f.unlearn.profession = entry.skill, entry.name
    f.unlearn:SetShown(f.bar:IsShown() and not entry.secondary)   -- no X for Cooking, Fishing, First Aid (nor on the client's page)
    local height = math.max(sectionH, 54 + math.max(1, #spells) * SPELL_STEP + 4)
    f:SetHeight(height)
    f:Show()
    return height
end

-- the client's page and its two primary sections, when this client has them
local function PageParts()
    local frame = _G.ProfessionsFrame
    local book = frame and type(frame.BookPage) == "table" and frame.BookPage or nil
    local holder = book and type(book.ProfessionsContentFrame) == "table" and book.ProfessionsContentFrame or nil
    local p1 = (holder and type(holder.PrimaryProfession1) == "table" and holder.PrimaryProfession1) or _G.PrimaryProfession1
    local p2 = (holder and type(holder.PrimaryProfession2) == "table" and holder.PrimaryProfession2) or _G.PrimaryProfession2
    local secondaries = {}
    for i = 1, 4 do
        local f = (holder and type(holder["SecondaryProfession" .. i]) == "table" and holder["SecondaryProfession" .. i]) or _G["SecondaryProfession" .. i]
        if type(f) == "table" and f.Hide then secondaries[#secondaries + 1] = f end
    end
    return book, holder, p1, p2, secondaries
end

local pending
local function Refresh()
    if InCombatLockdown() then pending = true return end
    pending = false
    local book, holder, p1, p2, secondaries = PageParts()
    if not (book and holder and p1 and p2) or not book:IsVisible() then return end
    -- the list covers the whole page: the two primary sections and the secondary row below them
    local covered = { p1, p2 }
    for _, f in ipairs(secondaries) do covered[#covered + 1] = f end
    if not list then
        list = TryCreate("ScrollFrame", "EverlakeProfessionsList", holder, "UIPanelScrollFrameTemplate")
        local bottom = secondaries[1] or p2
        list:SetPoint("TOPLEFT", p1, "TOPLEFT", 0, 0)
        list:SetPoint("TOPRIGHT", p1, "TOPRIGHT", -24, 0)   -- room for the scroll bar
        list:SetPoint("BOTTOMLEFT", bottom, "BOTTOMLEFT", 0, 0)
        list:EnableMouseWheel(true)
        list:SetScript("OnMouseWheel", function(self, delta)
            local range = self:GetVerticalScrollRange() or 0
            self:SetVerticalScroll(math.max(0, math.min(range, (self:GetVerticalScroll() or 0) - delta * 60)))
        end)
        content = CreateFrame("Frame", nil, list)
        list:SetScrollChild(content)
        -- the client shows its sections again as it refreshes the page: keep them covered
        for _, section in ipairs(covered) do
            section:HookScript("OnShow", function(self)
                if list and list:IsShown() and not InCombatLockdown() then self:Hide() end
            end)
        end
    end
    ClientArt(p1)
    if not secondaryArt or not next(secondaryArt) then secondaryArt = ReadSecondaryArt(secondaries) end
    local w, h = p1:GetSize()
    if w and w > 100 then sectionW = math.floor(w - 24) end
    if h and h > 60 then sectionH = math.floor(h) end

    local skillList = SkillListProfessions()
    local y, n = 0, 0
    for _, entry in ipairs(P.Known()) do
        n = n + 1
        local client = skillList[entry.skill]
        if client then   -- rank from the skill list when the addon's own lookup had none
            entry.rank = entry.rank or client.rank
            entry.maxRank = (entry.maxRank and entry.maxRank > 0) and entry.maxRank or client.maxRank
        end
        local spells = (client and (client.numSpells or 0) > 0) and BookSpells(client.index) or {}
        if #spells == 0 then spells = StaticSpells(entry) end
        local f = PrimarySection(n)
        f:ClearAllPoints()
        f:SetPoint("TOPLEFT", 0, -y)
        y = y + FillPrimary(f, entry, spells, p1) + SECTION_GAP
    end
    -- then Cooking, Fishing and First Aid, as the client lists them, with the spells its own page shows for them
    local _, clientSecondary = ClientProfessions()
    for _, entry in ipairs(clientSecondary) do
        n = n + 1
        entry.secondary = true
        local f = PrimarySection(n)
        f:ClearAllPoints()
        f:SetPoint("TOPLEFT", 0, -y)
        y = y + FillPrimary(f, entry, BookSpells(entry.index), p1) + SECTION_GAP
    end
    for i = n + 1, #sections do sections[i]:Hide() end
    content:SetSize(sectionW, math.max(1, y))
    for _, section in ipairs(covered) do section:Hide() end
    list:Show()
end

local hooked = {}
local function Hook()
    local book = PageParts()
    if book and not hooked[book] then
        hooked[book] = true
        book:HookScript("OnShow", function()
            Refresh()
            if C_Timer and C_Timer.After then C_Timer.After(0, Refresh) end   -- once the client has filled the page
        end)
    end
    local frame = _G.ProfessionsFrame
    if frame and not hooked[frame] then
        hooked[frame] = true
        frame:HookScript("OnShow", function()
            if C_Timer and C_Timer.After then C_Timer.After(0, Refresh) else Refresh() end
        end)
    end
end

-- /everlake prof page: the art of the client's first primary section (it is hidden under the list, but kept), to match
-- its look: every texture with atlas or file, layer and size, through its child frames and status bar.
local function DumpPage()
    local _, _, p1 = PageParts()
    if not p1 then print("No PrimaryProfession1 in this client.") return end
    local function tex(r)
        local atlas = r.GetAtlas and r:GetAtlas()
        local w, h = r:GetSize()
        local point, rel, relPoint, x, y = r:GetPoint(1)
        return string.format("%s %s %dx%d @%s %d,%d", r:GetDrawLayer() or "?", atlas and ("atlas " .. atlas) or tostring(r:GetTexture()),
            w or 0, h or 0, point or "-", x or 0, y or 0)
    end
    local function key(parent, child)
        for k, v in pairs(parent) do if v == child and type(k) == "string" then return k end end
        return child:GetName() or child:GetObjectType()
    end
    local function walk(frame, depth, label)
        if depth > 3 then return end
        local w, h = frame:GetSize()
        print(string.rep(" ", depth) .. label .. string.format(" (%s %dx%d)", frame:GetObjectType(), w or 0, h or 0))
        if frame.GetStatusBarTexture and frame:GetStatusBarTexture() then
            print(string.rep(" ", depth + 1) .. "bar: " .. tex(frame:GetStatusBarTexture()))
        end
        for _, r in ipairs({ frame:GetRegions() }) do
            if r:GetObjectType() == "Texture" and (r:GetAtlas() or r:GetTexture()) then print(string.rep(" ", depth + 1) .. tex(r)) end
        end
        for _, c in ipairs({ frame:GetChildren() }) do walk(c, depth + 1, key(frame, c)) end
    end
    walk(p1, 0, "PrimaryProfession1")
end
addon.DumpProfessionPage = DumpPage

-- /everlake prof bar: only the bar parts of the client's first section, and what was picked from them
local function DumpBar()
    local _, _, p1 = PageParts()
    if not p1 then print("No PrimaryProfession1 in this client.") return end
    local function walk(frame, depth, label)
        if depth > 5 then return end
        local isBar = label:find("Bar") or (frame.GetStatusBarTexture and true)
        if isBar then
            local w, h = frame:GetSize()
            print(string.rep(" ", depth) .. label .. string.format(" (%s %dx%d)", frame:GetObjectType(), w or 0, h or 0))
            if frame.GetStatusBarTexture and frame:GetStatusBarTexture() then
                local t = frame:GetStatusBarTexture()
                print(string.rep(" ", depth + 1) .. "fill: " .. tostring(t:GetAtlas() or t:GetTexture()))
            end
            for _, r in ipairs({ frame:GetRegions() }) do
                if r:GetObjectType() == "Texture" then
                    local w, h = r:GetSize()
                    local pt, rel, rp, x, y = r:GetPoint(1)
                    local relName = rel == frame and "parent" or (rel and (rel.GetName and rel:GetName()) or "other") or "-"
                    print(string.rep(" ", depth + 1) .. string.format("%s %s %dx%d %s>%s %s %d,%d %s", r:GetDrawLayer() or "?",
                        tostring(r:GetAtlas() or r:GetTexture()), w or 0, h or 0, pt or "-", rp or "-", relName, x or 0, y or 0,
                        r:IsShown() and "" or "hidden"))
                end
            end
        end
        for _, c in ipairs({ frame:GetChildren() }) do
            local key
            for k, v in pairs(frame) do if v == c and type(k) == "string" then key = k break end end
            walk(c, depth + 1, key or c:GetName() or c:GetObjectType())
        end
    end
    walk(p1, 0, "PrimaryProfession1")
    print("picked: card " .. tostring(art and art.cardName) .. ", fill " .. tostring(art and art.fill))
end
addon.DumpProfessionBar = DumpBar

local events = CreateFrame("Frame")
for _, e in ipairs({ "PLAYER_LOGIN", "ADDON_LOADED", "SKILL_LINES_CHANGED", "SPELLS_CHANGED", "LEARNED_SPELL_IN_TAB", "PLAYER_REGEN_ENABLED",
        "TRADE_SKILL_SHOW" }) do
    pcall(events.RegisterEvent, events, e)
end
events:SetScript("OnEvent", function(_, event)
    Hook()
    if event ~= "PLAYER_REGEN_ENABLED" or pending then Refresh() end
end)
