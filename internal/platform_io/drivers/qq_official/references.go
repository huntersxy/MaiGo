package qq_official

import (
	"sync"
	"time"
)

type messageScope struct {
	account string
	scene   string
	target  string
}

type scopedMessage struct {
	messageScope
	id string
}

type messageReference struct {
	at    time.Time
	index string
}

type passiveReply struct {
	at   time.Time
	used int
}

type messageReferences struct {
	mu                sync.Mutex
	references        map[scopedMessage]messageReference
	passive           map[scopedMessage]passiveReply
	proactiveSequence int
}

func (s *messageReferences) recordInbound(key scopedMessage, at time.Time, index, quotedIndex string) (quotedID string, inserted bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for candidate, reference := range s.references {
		if now.Sub(reference.at) >= 24*time.Hour {
			delete(s.references, candidate)
			continue
		}
		if quotedID == "" && quotedIndex != "" && candidate.messageScope == key.messageScope && reference.index == quotedIndex {
			quotedID = candidate.id
		}
	}
	if index != "" {
		s.storeReferenceLocked(key, at, index)
	}
	if s.passive == nil {
		s.passive = make(map[scopedMessage]passiveReply)
	}
	for candidate, reply := range s.passive {
		window, _ := passiveLimits(candidate.scene)
		if now.Sub(reply.at) >= window {
			delete(s.passive, candidate)
		}
	}
	if _, exists := s.passive[key]; !exists {
		s.passive[key] = passiveReply{at: at}
		inserted = true
	}
	return quotedID, inserted
}

func (s *messageReferences) discardPassive(key scopedMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.passive, key)
}

func (s *messageReferences) reference(key scopedMessage) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.references[key]
	if ok && time.Since(ref.at) < 24*time.Hour {
		return ref.index
	}
	return ""
}

func (s *messageReferences) recordSent(key scopedMessage, index string) {
	if index == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeReferenceLocked(key, time.Now(), index)
}

func (s *messageReferences) storeReferenceLocked(key scopedMessage, at time.Time, index string) {
	if s.references == nil {
		s.references = make(map[scopedMessage]messageReference)
	}
	s.references[key] = messageReference{at: at, index: index}
	if len(s.references) > 2000 {
		oldestKey, oldestTime := key, at
		for candidate, ref := range s.references {
			if ref.at.Before(oldestTime) {
				oldestKey, oldestTime = candidate, ref.at
			}
		}
		delete(s.references, oldestKey)
	}
}

func passiveLimits(scene string) (time.Duration, int) {
	if scene == "group" {
		return 5 * time.Minute, 5
	}
	return time.Hour, 4
}

func (s *messageReferences) sendFields(key scopedMessage) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fields := s.claimPassiveLocked(key); fields != nil {
		return fields
	}
	s.proactiveSequence = s.proactiveSequence%10000 + 1
	return map[string]any{"msg_seq": s.proactiveSequence}
}

func (s *messageReferences) claimPassive(key scopedMessage) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimPassiveLocked(key)
}

func (s *messageReferences) claimPassiveLocked(key scopedMessage) map[string]any {
	if reply, ok := s.passive[key]; ok {
		window, limit := passiveLimits(key.scene)
		if time.Since(reply.at) < window && reply.used < limit {
			reply.used++
			s.passive[key] = reply
			return map[string]any{"msg_id": key.id, "msg_seq": reply.used}
		}
	}
	return nil
}
