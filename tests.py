# -*- coding: utf-8 -*-
"""
End-to-end tests that flush the Redis database (number 8).
These tests expect to be able to talk to the running server on
localhost:8000.
"""

import unittest

from nose.tools import ok_, eq_
import redis
import requests


class E2E(unittest.TestCase):

    _base = 'http://localhost:3000'

    @classmethod
    def setUpClass(cls):
        cls.c = redis.StrictRedis(host='localhost', port=6379, db=8)

    def setUp(self):
        self.c.flushdb()
        assert self.c.dbsize() == 0, self.c.dbsize()
        self._set_domain_key('xyz123', 'peterbecom')

    def _set_domain_key(self, key, domain):
        self.c.hset('$domainkeys', key, domain)

    def get(self, url, *args, **kwargs):
        return requests.get(self._base + url, *args, **kwargs)

    def post(self, url, *args, **kwargs):
        return requests.post(self._base + url, *args, **kwargs)

    def delete(self, url, *args, **kwargs):
        return requests.delete(self._base + url, *args, **kwargs)

    def test_homepage(self):
        r = self.get('/')
        eq_(r.status_code, 200)

    def test_404(self):
        r = self.get('/gobblygook')
        eq_(r.status_code, 404)

    def test_post_bad_number(self):
        r = self.post('/v1', {
            'url': ' /plog/something   ',
            'popularity': "1.2.x",
            'title': "This is a blog about something",
            "groups": "private,public",
        }, headers={'Auth-Key': 'xyz123'})
        ok_(r.status_code >= 400 and r.status_code < 500)

    def test_bad_key(self):
        r = self.post('/v1', {
            'url': '/plog/something',
            'title': "This is a blog about something",
        }, headers={})  # not set at all
        eq_(r.status_code, 403)

        r = self.post('/v1', {
            'url': '/plog/something',
            'title': "This is a blog about something",
        }, headers={'Auth-Key': ''})  # empty
        eq_(r.status_code, 403)

        r = self.post('/v1', {
            'url': '/plog/something',
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'junkjunk'})  # junk
        eq_(r.status_code, 403)

    def test_post_ok(self):
        r = self.post('/v1', {
            'url': ' /plog/something   ',
            'popularity': "12",
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)

        r = self.get('/v1?q=blo&d=peterbecom')
        eq_(
            r.json(),
            {
                'terms': [u'blo'],
                'results': [
                    [
                        u'/plog/something',
                        u'This is a blog about something'
                    ]
                ]
            }
        )

    def test_different_domains(self):
        r = self.post('/v1', {
            'url': ' /plog/something   ',
            'popularity': "12",
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)

        # need a new auth key for this different domain
        self._set_domain_key('abc987', 'air.mozilla.org')
        r = self.post('/v1', {
            'url': ' /some/page',
            'title': "Also about the word blog",
        }, headers={'Auth-Key': 'abc987'})
        eq_(r.status_code, 201)

        r = self.get('/v1?q=blo&d=peterbecom')
        eq_(
            r.json(),
            {
                'terms': [u'blo'],
                'results': [
                    [
                        u'/plog/something',
                        u'This is a blog about something'
                    ]
                ]
            }
        )

        r = self.get('/v1?q=blo&d=air.mozilla.org')
        eq_(
            r.json(),
            {
                'terms': [u'blo'],
                'results': [
                    [
                        u'/some/page',
                        u'Also about the word blog'
                    ]
                ]
            }
        )

    def test_unidecode(self):
        r = self.post('/v1', {
            'url': ' /some/page',
            'title': u"Blögged about something else",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)

        r = self.get('/v1?q=blog&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(
            r.json(),
            {
                'terms': [u'blog'],
                'results': [
                    [
                        u'/some/page',
                        u'Blögged about something else'
                    ]
                ]
            }
        )

        r = self.get(u'/v1?q=bl\xf6g&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(
            r.json(),
            {
                'terms': [u'blög', u'blog'],
                'results': [
                    [
                        u'/some/page',
                        u'Blögged about something else'
                    ]
                ]
            }
        )

    def test_fetch_with_dfferent_n(self):
        self._set_domain_key('xyz123', 'peterbecom')
        for i in range(1, 20):
            r = self.post('/v1', {
                'url': '/%d' % i,
                'popularity': i,
                'title': u"Page %d" % i,
            }, headers={'Auth-Key': 'xyz123'})
            eq_(r.status_code, 201)

        r = self.get('/v1?q=pag&d=peterbecom')
        eq_(len(r.json()['results']), 10)

        r = self.get('/v1?q=pag&d=peterbecom&n=2')
        eq_(len(r.json()['results']), 2)

        r = self.get('/v1?q=pag&d=peterbecom&n=0')
        eq_(len(r.json()['results']), 10)

        r = self.get('/v1?q=pag&d=peterbecom&n=-1')
        eq_(len(r.json()['results']), 10)

        r = self.get('/v1?q=pag&d=peterbecom&n=x')
        ok_(r.status_code >= 400 and r.status_code < 500)

    def test_fetch_without_domain(self):
        r = self.get('/v1?q=pag')
        ok_(r.status_code >= 400 and r.status_code < 500)

    def test_sorted_by_popularity(self):
        self._set_domain_key('xyz123', 'peterbecom')
        r = self.post('/v1', {
            'url': '/minor',
            'popularity': "1.1",
            'title': u"Page Minor",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)
        r = self.post('/v1', {
            'url': '/major',
            'popularity': "2.7",
            'title': u"Page Major",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)

        r = self.get('/v1?q=pag&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(
            r.json()['results'],
            [[u'/major', u'Page Major'], [u'/minor', u'Page Minor']]
        )

        # insert the Minor one again but this time with a high popularity
        r = self.post('/v1', {
            'url': '/minor',
            'popularity': "3.0",
            'title': u"Page Minor",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)
        r = self.get('/v1?q=pag&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(
            r.json()['results'],
            [[u'/minor', u'Page Minor'], [u'/major', u'Page Major']]
        )

    def test_match_multiple_words(self):

        r = self.post('/v1', {
            'url': ' /plog/something   ',
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)

        r = self.get('/v1?q=blog%20ab&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(
            r.json()['terms'],
            ['blog', 'ab']
        )
        eq_(
            r.json()['results'],
            [[u'/plog/something', u'This is a blog about something']]
        )

    def test_clean_junk(self):
        r = self.get('/v1', params={
            'q': '[{(";.!peter?-.")}]',
            'd': 'peterbecom'
        })
        eq_(r.status_code, 200)
        eq_(
            r.json()['terms'],
            [u'peter']
        )
        eq_(r.json()['results'], [])

    def test_delete_bad_auth_key(self):
        # not even set
        r = self.delete('/v1', params={
            'url': ' /plog/something   ',
        })
        eq_(r.status_code, 403)

        # set but not recognized
        r = self.delete('/v1', params={
            'url': ' /plog/something   ',
        }, headers={'Auth-Key': 'junkjunkjunk'})
        eq_(r.status_code, 403)

    def test_delete_row(self):
        self._set_domain_key('xyz123', 'peterbecom')
        r = self.post('/v1', {
            'url': '/plog/something',
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)
        r = self.get('/v1?q=ab&d=peterbecom')
        eq_(r.status_code, 200)
        ok_(r.json()['results'])

        r = self.delete('/v1', params={
            'url': ' /plog/something   ',
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 204)
        r = self.get('/v1?q=ab&d=peterbecom')
        eq_(r.status_code, 200)
        ok_(not r.json()['results'])

    def test_delete_row_carefully(self):
        """deleting one item, by URL, shouldn't affect other entries"""
        self._set_domain_key('xyz123', 'peterbecom')
        # first one
        r = self.post('/v1', {
            'url': '/plog/something',
            'title': "This is a blog about something",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)
        # second one
        r = self.post('/v1', {
            'url': '/other/url',
            'title': "Another blog post about nothing",
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 201)
        r = self.get('/v1?q=ab&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 2)

        r = self.delete('/v1', params={
            'url': ' /plog/something   ',
        }, headers={'Auth-Key': 'xyz123'})
        eq_(r.status_code, 204)
        r = self.get('/v1?q=ab&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 1)
        eq_(r.json()['results'], [
            [u'/other/url', u'Another blog post about nothing']
        ])

    def test_search_with_groups(self):
        r = self.post('/v1', {
            'url': '/page/public',
            'popularity': 10,
            'title': 'This is a PUBLIC page',
            'groups': '',
        }, headers={'Auth-Key': 'xyz123'})
        r = self.post('/v1', {
            'url': '/page/private',
            'popularity': 20,
            'title': 'This is a PRIVATE page',
            'groups': 'private'
        }, headers={'Auth-Key': 'xyz123'})
        r = self.get('/v1?q=thi&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 1)

        r = self.get('/v1?q=thi&d=peterbecom&g=private')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 2)

    def test_search_with_whole_words(self):
        """if you search for 'four thi' it should find 'Four things'
        and 'this is four items'.
        But should it really find 'fourier thinking'?
        """
        r = self.post('/v1', {
            'url': '/page/first',
            'popularity': 1,
            'title': 'Four special things',
        }, headers={'Auth-Key': 'xyz123'})
        r = self.post('/v1', {
            'url': '/page/second',
            'popularity': 2,
            'title': 'This is four items',
        }, headers={'Auth-Key': 'xyz123'})
        r = self.post('/v1', {
            'url': '/page/third',
            'popularity': 3,
            'title': 'Fourier thinking',
        }, headers={'Auth-Key': 'xyz123'})

        r = self.get('/v1?q=four&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 3)

        r = self.get('/v1?q=four%20thin&d=peterbecom')
        eq_(r.status_code, 200)
        eq_(len(r.json()['results']), 1)